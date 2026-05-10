package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/yaml"

	"github.com/yuriy-kovalchuk/yk-talos-management/internal/config"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosnodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosnodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update

type TalosNodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Talos    TalosDialer
	Recorder record.EventRecorder
}

func (r *TalosNodeReconciler) setError(ctx context.Context, node *v1alpha1.TalosNode, err error) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	node.Status.Phase = v1alpha1.TalosNodePhaseError
	node.Status.RetryCount++
	node.Status.Message = err.Error()
	delay := config.GetRetryDelay(node.Status.RetryCount)
	l.Error(err, "apply config failed", "ip", node.Spec.NodeIP, "attempt", node.Status.RetryCount, "requeueAfter", delay)
	emitEvent(r.Recorder, node, corev1.EventTypeWarning, "ApplyFailed", err.Error())
	if updateErr := r.Status().Update(ctx, node); updateErr != nil {
		l.Error(updateErr, "update error status")
	}
	return ctrl.Result{RequeueAfter: delay}, nil
}

func (r *TalosNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	var node v1alpha1.TalosNode

	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	l.V(1).Info("Reconciling TalosNode", "name", node.Name, "ip", node.Spec.NodeIP, "generation", node.Generation)
	start := time.Now()
	defer func() { l.V(1).Info("reconcile done", "duration", time.Since(start)) }()

	if node.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &node)
	}

	talos.AddFinalizer(&node.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, &node); err != nil {
		return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
	}

	if isNodeUpToDate(&node) {
		if driftEnabled(&node) {
			return r.checkDrift(ctx, &node)
		}
		l.Info("Node up-to-date, drift detection disabled", "generation", node.Generation)
		return ctrl.Result{}, nil
	}

	if err := r.applyConfig(ctx, &node); err != nil {
		if talos.IsContextCancelled(err) {
			return ctrl.Result{}, nil
		}
		return r.setError(ctx, &node, err)
	}

	l.Info("Node configured", "ip", node.Spec.NodeIP)
	emitEvent(r.Recorder, &node, corev1.EventTypeNormal, "Applied", "Machine configuration applied successfully")
	if driftEnabled(&node) {
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}
	return ctrl.Result{}, nil
}

// isNodeUpToDate returns true when the node config has been successfully applied and the spec hasn't changed.
func isNodeUpToDate(node *v1alpha1.TalosNode) bool {
	return node.Status.ObservedGeneration == node.Generation &&
		node.Status.Phase == v1alpha1.TalosNodePhaseReady &&
		talos.HasCondition(node.Status.Conditions, v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionTrue)
}

// driftEnabled returns true when drift detection is on (the default when unset).
func driftEnabled(node *v1alpha1.TalosNode) bool {
	return node.Spec.DriftDetection == nil || *node.Spec.DriftDetection
}

const (
	etcdLeaveMaxAttempts = 3
	etcdLeaveRetryDelay  = 90 * time.Second
	driftCheckInterval   = 5 * time.Minute
)

// checkDrift fetches the running config from the node and compares it with the saved secret.
// Connection failures are treated as "node offline" — logged at Info and requeued silently.
func (r *TalosNodeReconciler) checkDrift(ctx context.Context, node *v1alpha1.TalosNode) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	talosconfigSecret, err := getSecret(ctx, r.Client, clusterTalosconfigName(node.Spec.ClusterRef), node.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
		}
		return ctrl.Result{}, err
	}

	savedSecret, err := getSecret(ctx, r.Client, nodeConfigName(node.Name), node.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
		}
		return ctrl.Result{}, err
	}

	conn, err := r.Talos.Dial(ctx, talosconfigSecret.Data["talosconfig"], node.Spec.NodeIP)
	if err != nil {
		l.Info("drift check: node unreachable, will retry", "ip", node.Spec.NodeIP, "requeueAfter", driftCheckInterval)
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}
	defer conn.Close() //nolint:errcheck

	remoteBytes, err := conn.GetMachineConfig(ctx, node.Spec.NodeIP)
	if err != nil {
		l.Error(err, "drift check: could not read remote config, will retry", "ip", node.Spec.NodeIP, "requeueAfter", driftCheckInterval)
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	drifted, err := configsDiffer(savedSecret.Data["config.yaml"], remoteBytes)
	if err != nil {
		l.Error(err, "drift check: comparison failed", "ip", node.Spec.NodeIP)
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	if !drifted {
		l.V(1).Info("drift check: config in sync", "ip", node.Spec.NodeIP)
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	l.Info("drift detected, re-applying config", "ip", node.Spec.NodeIP)
	emitEvent(r.Recorder, node, corev1.EventTypeWarning, "DriftDetected", "Node config drift detected, re-applying")

	// Force re-apply: clear observed generation so applyConfig treats this as an update.
	node.Status.ObservedGeneration = node.Generation - 1
	if err := r.applyConfig(ctx, node); err != nil {
		if talos.IsContextCancelled(err) {
			return ctrl.Result{}, nil
		}
		return r.setError(ctx, node, err)
	}
	return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
}

// configsDiffer returns true when the two YAML documents parse to different structures.
// Ignores comments, whitespace, and field ordering.
func configsDiffer(local, remote []byte) (bool, error) {
	var localMap, remoteMap map[string]interface{}
	if err := yaml.Unmarshal(local, &localMap); err != nil {
		return false, fmt.Errorf("parse local config: %w", err)
	}
	if err := yaml.Unmarshal(remote, &remoteMap); err != nil {
		return false, fmt.Errorf("parse remote config: %w", err)
	}
	return !reflect.DeepEqual(localMap, remoteMap), nil
}

func (r *TalosNodeReconciler) handleDeletion(ctx context.Context, node *v1alpha1.TalosNode) (ctrl.Result, error) {
	if !talos.ContainsFinalizer(node.Finalizers, talos.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}

	if node.Spec.Role == v1alpha1.TalosNodeRoleControlPlane {
		done, result, err := r.handleEtcdLeave(ctx, node)
		if !done {
			return result, err
		}
	}

	secret, err := getSecret(ctx, r.Client, nodeConfigName(node.Name), node.Namespace)
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err == nil {
		if delErr := r.Delete(ctx, secret); delErr != nil {
			return ctrl.Result{}, delErr
		}
	}

	node.Finalizers = talos.RemoveFinalizer(node.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, node); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("Node cleaned up", "name", node.Name)
	return ctrl.Result{}, nil
}

// handleEtcdLeave manages etcd membership removal for a departing ControlPlane node.
// Returns (true, _, _) when the caller may proceed to cleanup, (false, result, _) to requeue.
func (r *TalosNodeReconciler) handleEtcdLeave(ctx context.Context, node *v1alpha1.TalosNode) (bool, ctrl.Result, error) {
	l := log.FromContext(ctx)

	talosconfig, endpoints, skip, err := r.loadTalosconfig(ctx, node)
	if err != nil {
		return false, ctrl.Result{}, err
	}
	if skip {
		return true, ctrl.Result{}, nil
	}

	if node.Status.DeletionAttempts < etcdLeaveMaxAttempts {
		if err := r.tryEtcdLeave(ctx, node.Spec.NodeIP, talosconfig); err != nil {
			node.Status.DeletionAttempts++
			l.Error(err, "etcd leave failed, will retry", "ip", node.Spec.NodeIP, "attempt", node.Status.DeletionAttempts, "requeueAfter", etcdLeaveRetryDelay)
			if updateErr := r.Status().Update(ctx, node); updateErr != nil {
				l.Error(updateErr, "update deletion attempts status")
			}
			return false, ctrl.Result{RequeueAfter: etcdLeaveRetryDelay}, nil
		}
		l.Info("etcd leave succeeded", "ip", node.Spec.NodeIP)
		return true, ctrl.Result{}, nil
	}

	// Max graceful attempts exceeded — force-remove via a surviving peer.
	l.Info("etcd leave max attempts exceeded, escalating to force-remove", "ip", node.Spec.NodeIP, "attempts", node.Status.DeletionAttempts)
	peers := survivingPeers(endpoints, node.Spec.NodeIP)
	if len(peers) == 0 {
		l.Info("no surviving peers available, skipping etcd force-remove")
		return true, ctrl.Result{}, nil
	}
	conn, survivorEP, err := dialAny(ctx, r.Talos, talosconfig, peers)
	if err != nil {
		l.Error(err, "could not dial surviving peer for etcd force-remove, proceeding with cleanup", "node", node.Name)
		return true, ctrl.Result{}, nil
	}
	defer conn.Close() //nolint:errcheck
	if err := conn.EtcdForceRemove(ctx, survivorEP, node.Spec.NodeIP); err != nil {
		l.Error(err, "etcd force-remove failed, proceeding with cleanup", "node", node.Name, "survivor", survivorEP)
	}
	return true, ctrl.Result{}, nil
}

// loadTalosconfig resolves the cluster and its talosconfig secret for the given node.
// Returns skip=true when the cluster or secret is not found (caller should proceed to cleanup).
func (r *TalosNodeReconciler) loadTalosconfig(ctx context.Context, node *v1alpha1.TalosNode) (talosconfig []byte, endpoints []string, skip bool, err error) {
	l := log.FromContext(ctx)

	cluster := &v1alpha1.TalosCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Spec.ClusterRef, Namespace: node.Namespace}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("cluster not found, skipping etcd leave", "node", node.Name, "cluster", node.Spec.ClusterRef)
			return nil, nil, true, nil
		}
		return nil, nil, false, err
	}

	secret, err := getSecret(ctx, r.Client, clusterTalosconfigName(cluster.Name), node.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("talosconfig secret not found, skipping etcd leave", "node", node.Name)
			return nil, nil, true, nil
		}
		return nil, nil, false, err
	}

	return secret.Data["talosconfig"], cluster.Spec.Endpoints, false, nil
}

func (r *TalosNodeReconciler) tryEtcdLeave(ctx context.Context, nodeIP string, talosconfig []byte) error {
	conn, err := r.Talos.Dial(ctx, talosconfig, nodeIP)
	if err != nil {
		return fmt.Errorf("dial node: %w", err)
	}
	defer conn.Close() //nolint:errcheck
	return conn.EtcdLeave(ctx, nodeIP)
}

// survivingPeers returns all endpoints except the one matching excludeIP.
func survivingPeers(endpoints []string, excludeIP string) []string {
	var peers []string
	for _, ep := range endpoints {
		if ep != excludeIP {
			peers = append(peers, ep)
		}
	}
	return peers
}

func (r *TalosNodeReconciler) applyConfig(ctx context.Context, node *v1alpha1.TalosNode) error {
	firstApply := !talos.HasCondition(node.Status.Conditions, v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionTrue)

	node.Status.ObservedGeneration = node.Generation
	node.Status.Phase = v1alpha1.TalosNodePhaseApplying
	if err := r.Status().Update(ctx, node); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	cluster := &v1alpha1.TalosCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Spec.ClusterRef, Namespace: node.Namespace}, cluster); err != nil {
		return fmt.Errorf("get cluster: %w", err)
	}

	secretName, key := clusterControlPlaneName(cluster.Name), "controlplane.yaml"
	if node.Spec.Role == v1alpha1.TalosNodeRoleWorker {
		secretName, key = clusterWorkerName(cluster.Name), "worker.yaml"
	}

	configSecret, err := getSecret(ctx, r.Client, secretName, node.Namespace)
	if err != nil {
		return fmt.Errorf("get config secret: %w", err)
	}

	var baseConfig map[string]interface{}
	if err := yaml.Unmarshal(configSecret.Data[key], &baseConfig); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}

	var standalonePatches []string
	for _, patch := range node.Spec.Patches {
		var p map[string]interface{}
		if err := yaml.Unmarshal([]byte(patch), &p); err != nil {
			return fmt.Errorf("unmarshal patch: %w", err)
		}
		// Talos extension documents (RegistryMirrorConfig, KubeletConfig, …) carry
		// an apiVersion field. Everything else is a machine/cluster config patch and
		// should be deep-merged into the base config.
		if _, isExtension := p["apiVersion"]; isExtension {
			standalonePatches = append(standalonePatches, strings.TrimSpace(patch))
		} else {
			baseConfig = mergePatches(baseConfig, p)
		}
	}

	for _, ref := range node.Spec.PatchesFrom {
		secret, err := getSecret(ctx, r.Client, ref.Name, node.Namespace)
		if err != nil {
			return fmt.Errorf("get patch secret %q: %w", ref.Name, err)
		}
		raw, ok := secret.Data[ref.Key]
		if !ok {
			return fmt.Errorf("patch secret %q has no key %q", ref.Name, ref.Key)
		}
		var p map[string]interface{}
		if err := yaml.Unmarshal(raw, &p); err != nil {
			return fmt.Errorf("unmarshal patch from secret %q key %q: %w", ref.Name, ref.Key, err)
		}
		if _, isExtension := p["apiVersion"]; isExtension {
			standalonePatches = append(standalonePatches, strings.TrimSpace(string(raw)))
		} else {
			baseConfig = mergePatches(baseConfig, p)
		}
	}

	configBytes, err := yaml.Marshal(baseConfig)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	for _, doc := range standalonePatches {
		configBytes = append(configBytes, "\n---\n"...)
		configBytes = append(configBytes, doc...)
		configBytes = append(configBytes, '\n')
	}

	var conn TalosConnection
	if firstApply {
		conn, err = r.Talos.DialInsecure(ctx, node.Spec.NodeIP)
	} else {
		talosconfigSecret, tcErr := getSecret(ctx, r.Client, clusterTalosconfigName(cluster.Name), node.Namespace)
		if tcErr != nil {
			return fmt.Errorf("get talosconfig secret: %w", tcErr)
		}
		conn, err = r.Talos.Dial(ctx, talosconfigSecret.Data["talosconfig"], node.Spec.NodeIP)
	}
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	if err := conn.ApplyConfig(ctx, node.Spec.NodeIP, configBytes); err != nil {
		return fmt.Errorf("apply config: %w", err)
	}

	if err := r.saveNodeConfig(ctx, node, configBytes); err != nil {
		return fmt.Errorf("save node config: %w", err)
	}

	node.Status.Phase = v1alpha1.TalosNodePhaseReady
	node.Status.Message = "Configuration applied"
	talos.SetConditionStatus(&node.Status.Conditions,
		v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionTrue, "Applied", "Configuration applied successfully")
	now := metav1.Now()
	node.Status.LastUpdateTime = &now
	return r.Status().Update(ctx, node)
}

// saveNodeConfig persists the final merged machine config (base + patches) to a secret so it
// can be inspected for debugging and used for drift detection in the future.
func (r *TalosNodeReconciler) saveNodeConfig(ctx context.Context, node *v1alpha1.TalosNode, configBytes []byte) error {
	name := nodeConfigName(node.Name)
	return upsertSecret(ctx, r.Client, name, node.Namespace,
		func() *corev1.Secret { return newSecret(name, node.Namespace, "config.yaml", configBytes) },
		func(s *corev1.Secret) { s.Data = map[string][]byte{"config.yaml": configBytes} },
	)
}

// mergePatches deep-merges patch into base, with patch values taking precedence.
func mergePatches(base, patch map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(base))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range patch {
		if baseMap, ok := result[k].(map[string]interface{}); ok {
			if patchMap, ok := v.(map[string]interface{}); ok {
				result[k] = mergePatches(baseMap, patchMap)
				continue
			}
		}
		result[k] = v
	}
	return result
}

func (r *TalosNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TalosNode{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
