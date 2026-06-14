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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/yaml"

	"github.com/yuriy-kovalchuk/yk-talos-management/internal/config"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/factory"
	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosnodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosnodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;delete

type TalosNodeReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Talos           TalosDialer
	Recorder        record.EventRecorder
	NewRemoteClient func(kubeconfig []byte) (kubernetes.Interface, error)
	// Factory creates Image Factory schematics for nodes with spec.systemExtensions.
	// Injected in tests; production uses factory.New() set in run.go.
	// When nil and spec.systemExtensions is non-empty the reconcile returns an error.
	Factory factory.Client
}

func (r *TalosNodeReconciler) setError(ctx context.Context, node *v1alpha1.TalosNode, err error) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	fromPhase := node.Status.Phase
	node.Status.Phase = v1alpha1.TalosNodePhaseError
	node.Status.RetryCount++
	node.Status.Message = err.Error()
	delay := config.GetRetryDelay(node.Status.RetryCount)
	l.Error(err, "apply config failed", "ip", node.Spec.NodeIP, "attempt", node.Status.RetryCount, "requeueAfter", delay.String())
	appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP, string(fromPhase), string(v1alpha1.TalosNodePhaseError))
	appmetrics.ConfigApplyTotal.WithLabelValues(string(node.Spec.Role), "error", node.Spec.ClusterRef).Inc()
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

	l.V(1).Info("reconciling TalosNode", "ip", node.Spec.NodeIP, "generation", node.Generation)
	start := time.Now()
	defer func() { l.V(1).Info("reconcile done", "duration", time.Since(start)) }()
	appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP, string(node.Status.Phase), string(node.Status.Phase))
	r.refreshConfigSizeMetric(ctx, &node)

	if node.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &node)
	}

	if err := ensureFinalizer(ctx, r.Client, &node); err != nil {
		return ctrl.Result{}, err
	}

	// Upgrade check: initiated by the talos.yuriykovalchuk.dev/upgrade annotation.
	// Phase check comes first so we don't re-trigger on the same annotation while
	// the node is rebooting after the upgrade RPC was already sent.
	if node.Status.Phase == v1alpha1.TalosNodePhaseUpgrading {
		return r.checkUpgradeComplete(ctx, &node)
	}
	// Annotation-based upgrade (imperative escape hatch, highest priority).
	if v := node.Annotations[talos.AnnotationUpgrade]; v != "" && v != node.Annotations[talos.AnnotationLastUpgrade] {
		return r.handleUpgrade(ctx, &node, v)
	}
	// Spec-driven upgrade: spec.talosVersion and/or spec.systemExtensions changed.
	if result, done, err := r.reconcileVersion(ctx, &node); done {
		return result, err
	}

	// Standalone reset: annotation triggers a one-shot wipe+reboot to maintenance mode.
	// GitOps-safe: the companion last-reset annotation records the processed request ID
	// so ArgoCD/Flux re-adding the annotation does not cause an infinite loop.
	if v := node.Annotations[talos.AnnotationReset]; v != "" && v != node.Annotations[talos.AnnotationLastReset] {
		return r.handleStandaloneReset(ctx, &node)
	}

	if isNodeUpToDate(&node) {
		if driftEnabled(&node) {
			return r.checkDrift(ctx, &node)
		}
		l.Info("node up-to-date, drift detection disabled", "generation", node.Generation)
		return ctrl.Result{}, nil
	}

	if err := r.applyConfig(ctx, &node); err != nil {
		if talos.IsContextCancelled(err) {
			return ctrl.Result{}, nil
		}
		return r.setError(ctx, &node, err)
	}

	l.Info("node configured", "ip", node.Spec.NodeIP)
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

// checkDrift fetches the running config from the node and compares it with the saved secret.
// Connection failures are treated as "node offline" — logged at Info and requeued silently.
func (r *TalosNodeReconciler) checkDrift(ctx context.Context, node *v1alpha1.TalosNode) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	talosconfigSecret, skip, err := getSecretOrSkip(ctx, r.Client, clusterTalosconfigName(node.Spec.ClusterRef), node.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if skip {
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	savedSecret, skip, err := getSecretOrSkip(ctx, r.Client, nodeConfigName(node.Name), node.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if skip {
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	talosconfigBytes, err := secretKey(talosconfigSecret, "talosconfig")
	if err != nil {
		l.Error(err, "drift check: malformed talosconfig secret", "ip", node.Spec.NodeIP)
		appmetrics.DriftCheckTotal.WithLabelValues("error", node.Spec.ClusterRef, node.Name).Inc()
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	conn, err := r.Talos.Dial(ctx, talosconfigBytes, node.Spec.NodeIP)
	if err != nil {
		l.Info("drift check: node unreachable, will retry", "ip", node.Spec.NodeIP, "requeueAfter", driftCheckInterval.String())
		appmetrics.DriftCheckTotal.WithLabelValues("unreachable", node.Spec.ClusterRef, node.Name).Inc()
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}
	defer conn.Close() //nolint:errcheck

	remoteBytes, err := conn.GetMachineConfig(ctx, node.Spec.NodeIP)
	if err != nil {
		l.Error(err, "drift check: could not read remote config, will retry", "ip", node.Spec.NodeIP, "requeueAfter", driftCheckInterval.String())
		appmetrics.DriftCheckTotal.WithLabelValues("error", node.Spec.ClusterRef, node.Name).Inc()
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	savedConfig, err := secretKey(savedSecret, "config.yaml")
	if err != nil {
		l.Error(err, "drift check: malformed saved config secret", "ip", node.Spec.NodeIP)
		appmetrics.DriftCheckTotal.WithLabelValues("error", node.Spec.ClusterRef, node.Name).Inc()
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	drifted, err := configsDiffer(savedConfig, remoteBytes)
	if err != nil {
		l.Error(err, "drift check: comparison failed", "ip", node.Spec.NodeIP)
		appmetrics.DriftCheckTotal.WithLabelValues("error", node.Spec.ClusterRef, node.Name).Inc()
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	if !drifted {
		l.V(1).Info("drift check: config in sync", "ip", node.Spec.NodeIP)
		appmetrics.DriftCheckTotal.WithLabelValues("in_sync", node.Spec.ClusterRef, node.Name).Inc()
		return ctrl.Result{RequeueAfter: driftCheckInterval}, nil
	}

	appmetrics.DriftCheckTotal.WithLabelValues("drifted", node.Spec.ClusterRef, node.Name).Inc()
	l.Info("drift detected, re-applying config", "ip", node.Spec.NodeIP)
	emitEvent(r.Recorder, node, corev1.EventTypeWarning, "DriftDetected", "Node config drift detected, re-applying")

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

	l := log.FromContext(ctx)

	// Transition to Deleting on first entry so the phase reflects progress during
	// what can be a multi-minute drain + etcd-leave sequence.
	if node.Status.Phase != v1alpha1.TalosNodePhaseDeleting {
		fromPhase := node.Status.Phase
		node.Status.Phase = v1alpha1.TalosNodePhaseDeleting
		appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP, string(fromPhase), string(v1alpha1.TalosNodePhaseDeleting))
		if err := r.Status().Update(ctx, node); err != nil {
			return ctrl.Result{}, fmt.Errorf("update deleting phase: %w", err)
		}
	}

	// Guard: refuse to delete the last active ControlPlane in the cluster.
	// Removing the last CP destroys etcd quorum and the API server — the user must
	// add a replacement CP first, or delete the entire TalosCluster object instead.
	if node.Spec.Role == v1alpha1.TalosNodeRoleControlPlane {
		last, err := r.isLastControlPlane(ctx, node)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("check last control plane: %w", err)
		}
		if last {
			l.Info("deletion blocked: this is the last ControlPlane node — add a replacement CP first, or delete the TalosCluster to tear down the entire cluster",
				"cluster", node.Spec.ClusterRef)
			return ctrl.Result{RequeueAfter: deletionGuardRequeueDelay}, nil
		}
	}

	if skipDrain(node) {
		l.V(1).Info("drain skipped", "reason", drainSkipReason(node))
		appmetrics.NodeDrainTotal.WithLabelValues("skipped", node.Spec.ClusterRef).Inc()
	} else {
		done, result, err := r.drainAndDeleteNode(ctx, node)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !done {
			return result, nil
		}
	}

	if node.Spec.Role == v1alpha1.TalosNodeRoleControlPlane {
		done, result, err := r.handleEtcdLeave(ctx, node)
		if !done {
			return result, err
		}
	}

	// Reset-on-delete: wipe the node before cleanup so it returns to maintenance mode.
	// Best-effort — failure is logged and emits an event but never blocks deletion.
	// graceful=false: node may already be degraded after drain + etcd leave.
	if node.Spec.ResetOnDelete {
		emitEvent(r.Recorder, node, corev1.EventTypeNormal, "NodeResetTriggered", "Resetting node before cleanup (spec.resetOnDelete)")
		if err := r.tryReset(ctx, node, false); err != nil {
			l.Error(err, "reset-on-delete failed, proceeding with cleanup", "ip", node.Spec.NodeIP)
			emitEvent(r.Recorder, node, corev1.EventTypeWarning, "NodeResetFailed", fmt.Sprintf("reset-on-delete failed: %v", err))
		} else {
			l.Info("node reset complete", "ip", node.Spec.NodeIP)
			emitEvent(r.Recorder, node, corev1.EventTypeNormal, "NodeResetComplete", "Node reset successfully")
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

	// For ControlPlane nodes: remove the dead IP from TalosCluster.spec.endpoints, then
	// update the kubeconfig Secret so its server URL points to a surviving endpoint.
	// Both steps are best-effort — failures are logged but do not block finalizer removal.
	if node.Spec.Role == v1alpha1.TalosNodeRoleControlPlane {
		if err := r.removeEndpointFromCluster(ctx, node); err != nil {
			l.Error(err, "could not remove endpoint from TalosCluster, proceeding with cleanup", "ip", node.Spec.NodeIP)
		} else {
			l.V(1).Info("removed endpoint from TalosCluster", "ip", node.Spec.NodeIP, "cluster", node.Spec.ClusterRef)
		}
		if err := r.refreshKubeconfig(ctx, node); err != nil {
			l.Error(err, "could not refresh kubeconfig Secret server URL", "cluster", node.Spec.ClusterRef)
		} else {
			l.V(1).Info("kubeconfig Secret updated to surviving endpoint", "cluster", node.Spec.ClusterRef)
		}
	}

	node.Finalizers = talos.RemoveFinalizer(node.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, node); err != nil {
		return ctrl.Result{}, err
	}
	l.Info("node cleaned up")
	return ctrl.Result{}, nil
}

// handleStandaloneReset performs a one-shot reset triggered by the
// talos.yuriykovalchuk.dev/reset annotation. The annotation value is treated as a
// request ID — any non-empty string works ("true", a UUID, a timestamp).
//
// GitOps-safe: the companion annotation last-reset is set to the request ID BEFORE
// calling Reset so that:
//   - A controller crash during reset does not loop (last-reset matches → skip).
//   - ArgoCD/Flux re-adding the annotation does not cause a second reset (same ID).
//
// To trigger a second reset, change the annotation value to a new unique string.
// On success, ConfigApplied is cleared so the next reconcile re-applies config.
func (r *TalosNodeReconciler) handleStandaloneReset(ctx context.Context, node *v1alpha1.TalosNode) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	// Record last-reset = current reset ID before calling Reset.
	// This prevents both crash-loop and GitOps re-add loops.
	resetID := node.Annotations[talos.AnnotationReset]
	if err := patchAnnotations(ctx, r.Client, node, map[string]string{
		talos.AnnotationLastReset: resetID,
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("set last-reset annotation: %w", err)
	}

	emitEvent(r.Recorder, node, corev1.EventTypeNormal, "NodeResetTriggered", "Node reset triggered via annotation")
	l.Info("reset triggered via annotation", "ip", node.Spec.NodeIP)

	if err := r.tryReset(ctx, node, true); err != nil {
		l.Error(err, "node reset failed", "ip", node.Spec.NodeIP)
		emitEvent(r.Recorder, node, corev1.EventTypeWarning, "NodeResetFailed", err.Error())
		return ctrl.Result{}, nil
	}

	l.Info("node reset, config will be re-applied on next reconcile", "ip", node.Spec.NodeIP)
	emitEvent(r.Recorder, node, corev1.EventTypeNormal, "NodeResetComplete", "Node reset successfully; config will be re-applied on next reconcile")

	// Clear ConfigApplied so isNodeUpToDate returns false and applyConfig runs again.
	// firstApply will be true (maintenance mode) → DialInsecure is used.
	talos.SetConditionStatus(&node.Status.Conditions,
		v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionFalse, "Reset", "Node was reset; waiting for config re-apply")
	fromPhase := node.Status.Phase
	node.Status.Phase = v1alpha1.TalosNodePhasePending
	appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP, string(fromPhase), string(v1alpha1.TalosNodePhasePending))
	if err := r.Status().Update(ctx, node); err != nil {
		l.Error(err, "update status after reset")
	}
	return ctrl.Result{}, nil
}

// withConn dials endpoint via mTLS, calls op, then closes. Centralises the
// dial → defer-close → call pattern used by single-operation helpers.
func (r *TalosNodeReconciler) withConn(ctx context.Context, talosconfig []byte, endpoint string, op func(TalosConnection) error) error {
	conn, err := r.Talos.Dial(ctx, talosconfig, endpoint)
	if err != nil {
		return fmt.Errorf("dial %s: %w", endpoint, err)
	}
	defer conn.Close() //nolint:errcheck
	return op(conn)
}

// tryReset dials the node via mTLS and issues a reset (wipe + reboot to maintenance mode).
// graceful=true stops services cleanly first (for healthy nodes); false skips service
// shutdown (for nodes that may already be degraded after drain).
func (r *TalosNodeReconciler) tryReset(ctx context.Context, node *v1alpha1.TalosNode, graceful bool) error {
	talosconfig, _, skip, err := r.loadTalosconfig(ctx, node)
	if err != nil {
		return fmt.Errorf("load talosconfig: %w", err)
	}
	if skip {
		return fmt.Errorf("talosconfig or cluster not found")
	}
	return r.withConn(ctx, talosconfig, node.Spec.NodeIP, func(conn TalosConnection) error {
		return conn.Reset(ctx, node.Spec.NodeIP, graceful)
	})
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
			l.Error(err, "etcd leave failed, will retry", "ip", node.Spec.NodeIP, "attempt", node.Status.DeletionAttempts, "requeueAfter", etcdLeaveRetryDelay.String())
			appmetrics.EtcdLeaveTotal.WithLabelValues("failed", node.Spec.ClusterRef).Inc()
			if updateErr := r.Status().Update(ctx, node); updateErr != nil {
				l.Error(updateErr, "update deletion attempts status")
			}
			return false, ctrl.Result{RequeueAfter: etcdLeaveRetryDelay}, nil
		}
		l.Info("etcd leave succeeded", "ip", node.Spec.NodeIP)
		appmetrics.EtcdLeaveTotal.WithLabelValues("success", node.Spec.ClusterRef).Inc()
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
		l.Error(err, "etcd force-remove failed, proceeding with cleanup", "survivor", survivorEP)
		appmetrics.EtcdLeaveTotal.WithLabelValues("force_remove_failed", node.Spec.ClusterRef).Inc()
	} else {
		appmetrics.EtcdLeaveTotal.WithLabelValues("force_removed", node.Spec.ClusterRef).Inc()
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

	talosconfigBytes, err := secretKey(secret, "talosconfig")
	if err != nil {
		return nil, nil, false, fmt.Errorf("read talosconfig secret: %w", err)
	}
	return talosconfigBytes, cluster.Spec.Endpoints, false, nil
}

func (r *TalosNodeReconciler) tryEtcdLeave(ctx context.Context, nodeIP string, talosconfig []byte) error {
	return r.withConn(ctx, talosconfig, nodeIP, func(conn TalosConnection) error {
		return conn.EtcdLeave(ctx, nodeIP)
	})
}

// survivingPeers returns all endpoints except the one matching excludeIP.
func survivingPeers(endpoints []string, excludeIP string) []string {
	return filterExclude(endpoints, excludeIP)
}

// removeEndpointFromCluster removes nodeIP from TalosCluster.spec.endpoints.
// Called after a ControlPlane node is fully removed so the dead IP does not
// linger in the cluster manifest or in configs generated for future nodes.
// Best-effort: the caller logs and proceeds on error so deletion is never blocked.
func (r *TalosNodeReconciler) removeEndpointFromCluster(ctx context.Context, node *v1alpha1.TalosNode) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cluster := &v1alpha1.TalosCluster{}
		if err := r.Get(ctx, types.NamespacedName{Name: node.Spec.ClusterRef, Namespace: node.Namespace}, cluster); err != nil {
			if apierrors.IsNotFound(err) {
				return nil // cluster already gone — nothing to do
			}
			return err
		}

		updated := filterExclude(cluster.Spec.Endpoints, node.Spec.NodeIP)

		if len(updated) == len(cluster.Spec.Endpoints) {
			return nil // IP was not in the list
		}
		if len(updated) == 0 {
			return nil // removing the last endpoint would leave an invalid cluster; skip
		}

		cluster.Spec.Endpoints = updated
		return r.Update(ctx, cluster)
	})
}

// isLastControlPlane returns true when node is the only active (non-terminating)
// ControlPlane TalosNode for its cluster. Used to block accidental last-CP deletion.
//
// Returns false immediately when the TalosCluster no longer exists: the guard
// protects etcd quorum, but if the cluster object is already gone there is no
// quorum left to protect. Allowing deletion in this case unblocks the recovery
// path when a user accidentally deleted the TalosCluster before its nodes.
func (r *TalosNodeReconciler) isLastControlPlane(ctx context.Context, node *v1alpha1.TalosNode) (bool, error) {
	cluster := &v1alpha1.TalosCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Spec.ClusterRef, Namespace: node.Namespace}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil // cluster gone — guard has nothing to protect
		}
		return false, fmt.Errorf("get cluster: %w", err)
	}

	var list v1alpha1.TalosNodeList
	if err := r.List(ctx, &list, client.InNamespace(node.Namespace)); err != nil {
		return false, fmt.Errorf("list TalosNodes: %w", err)
	}
	for _, n := range list.Items {
		if n.Name == node.Name {
			continue
		}
		if n.Spec.ClusterRef != node.Spec.ClusterRef {
			continue
		}
		if n.Spec.Role != v1alpha1.TalosNodeRoleControlPlane {
			continue
		}
		if n.DeletionTimestamp != nil {
			continue // peer is also being deleted — does not count
		}
		return false, nil // found a surviving CP
	}
	return true, nil
}

// refreshKubeconfig reads the cluster's kubeconfig Secret and rewrites the server
// URL to point to the first surviving control-plane endpoint.
//
// Called after a CP node is deleted. The credentials in the kubeconfig (CA, client
// cert/key) are cluster-wide and remain valid; only the server address needs to
// change if the deleted node was the endpoint the kubeconfig was pointing at.
//
// Best-effort: the caller logs and proceeds on error.
func (r *TalosNodeReconciler) refreshKubeconfig(ctx context.Context, node *v1alpha1.TalosNode) error {
	// Re-read the cluster to pick up the endpoint list AFTER removeEndpointFromCluster.
	cluster := &v1alpha1.TalosCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Spec.ClusterRef, Namespace: node.Namespace}, cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if len(cluster.Spec.Endpoints) == 0 {
		return nil
	}

	kubeconfigSecret, err := getSecret(ctx, r.Client, clusterKubeconfigName(node.Spec.ClusterRef), node.Namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // bootstrap never completed — nothing to update
		}
		return err
	}

	kubeconfigBytes, err := secretKey(kubeconfigSecret, "kubeconfig")
	if err != nil {
		return fmt.Errorf("read kubeconfig: %w", err)
	}
	updated, err := updateKubeconfigServer(kubeconfigBytes, cluster.Spec.Endpoints[0])
	if err != nil {
		return fmt.Errorf("rewrite kubeconfig server: %w", err)
	}

	kubeconfigSecret.Data["kubeconfig"] = updated
	return r.Update(ctx, kubeconfigSecret)
}

// updateKubeconfigServer parses a kubeconfig, sets every cluster's server URL to
// https://<endpoint>:6443, and re-serialises it.
func updateKubeconfigServer(kubeconfigBytes []byte, endpoint string) ([]byte, error) {
	cfg, err := clientcmd.Load(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	for _, c := range cfg.Clusters {
		c.Server = "https://" + endpoint + ":6443"
	}
	return clientcmd.Write(*cfg)
}

func (r *TalosNodeReconciler) applyConfig(ctx context.Context, node *v1alpha1.TalosNode) error {
	firstApply := !talos.HasCondition(node.Status.Conditions, v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionTrue)

	fromPhase := node.Status.Phase
	node.Status.ObservedGeneration = node.Generation
	node.Status.Phase = v1alpha1.TalosNodePhaseApplying
	if err := r.Status().Update(ctx, node); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP, string(fromPhase), string(v1alpha1.TalosNodePhaseApplying))

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

	baseConfigBytes, err := secretKey(configSecret, key)
	if err != nil {
		return fmt.Errorf("read base config: %w", err)
	}
	var baseConfig map[string]interface{}
	if err := yaml.Unmarshal(baseConfigBytes, &baseConfig); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}

	var standalonePatches []string
	for _, patch := range node.Spec.Patches {
		var err error
		baseConfig, err = applyRawPatch([]byte(patch), baseConfig, &standalonePatches)
		if err != nil {
			return fmt.Errorf("apply inline patch: %w", err)
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
		baseConfig, err = applyRawPatch(raw, baseConfig, &standalonePatches)
		if err != nil {
			return fmt.Errorf("apply patch from secret %q key %q: %w", ref.Name, ref.Key, err)
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

	talosconfigSecret, tcErr := getSecret(ctx, r.Client, clusterTalosconfigName(cluster.Name), node.Namespace)
	var talosconfigBytes []byte
	if tcErr == nil {
		talosconfigBytes, tcErr = secretKey(talosconfigSecret, "talosconfig")
	}

	var conn TalosConnection
	if firstApply {
		conn, err = r.Talos.DialInsecure(ctx, node.Spec.NodeIP)
		if err != nil && tcErr == nil {
			// Node may already be running (e.g. after CR recreation) — fall back to mTLS.
			conn, err = r.Talos.Dial(ctx, talosconfigBytes, node.Spec.NodeIP)
		}
	} else {
		if tcErr != nil {
			return fmt.Errorf("get talosconfig secret: %w", tcErr)
		}
		conn, err = r.Talos.Dial(ctx, talosconfigBytes, node.Spec.NodeIP)
	}
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	defer conn.Close() //nolint:errcheck

	if err := conn.ApplyConfig(ctx, node.Spec.NodeIP, configBytes, cluster.Name); err != nil {
		return fmt.Errorf("apply config: %w", err)
	}

	if err := r.saveNodeConfig(ctx, node, configBytes); err != nil {
		return fmt.Errorf("save node config: %w", err)
	}

	node.Status.Phase = v1alpha1.TalosNodePhaseReady
	node.Status.Message = "Configuration applied"
	node.Status.RetryCount = 0
	talos.SetConditionStatus(&node.Status.Conditions,
		v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionTrue, "Applied", "Configuration applied successfully")
	now := metav1.Now()
	node.Status.LastUpdateTime = &now
	if err := r.Status().Update(ctx, node); err != nil {
		return err
	}
	appmetrics.RecordNodePhase(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP, string(v1alpha1.TalosNodePhaseApplying), string(v1alpha1.TalosNodePhaseReady))
	appmetrics.ConfigApplyTotal.WithLabelValues(string(node.Spec.Role), "success", node.Spec.ClusterRef).Inc()
	return nil
}

// refreshConfigSizeMetric re-emits NodeConfigSizeBytes from the persisted config Secret.
// Called on every reconcile so the gauge survives operator pod restarts.
func (r *TalosNodeReconciler) refreshConfigSizeMetric(ctx context.Context, node *v1alpha1.TalosNode) {
	s, err := getSecret(ctx, r.Client, nodeConfigName(node.Name), node.Namespace)
	if err != nil {
		return
	}
	if size := len(s.Data["config.yaml"]); size > 0 {
		appmetrics.NodeConfigSizeBytes.WithLabelValues(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP).Set(float64(size))
	}
}

// saveNodeConfig persists the final merged machine config (base + patches) to a secret so it
// can be inspected for debugging and used for drift detection in the future.
func (r *TalosNodeReconciler) saveNodeConfig(ctx context.Context, node *v1alpha1.TalosNode, configBytes []byte) error {
	appmetrics.NodeConfigSizeBytes.WithLabelValues(node.Name, node.Namespace, node.Spec.ClusterRef, string(node.Spec.Role), node.Spec.NodeIP).Set(float64(len(configBytes)))
	name := nodeConfigName(node.Name)
	return upsertSecret(ctx, r.Client, name, node.Namespace,
		func() *corev1.Secret { return newSecret(name, node.Namespace, "config.yaml", configBytes) },
		func(s *corev1.Secret) { s.Data = map[string][]byte{"config.yaml": configBytes} },
	)
}

// applyRawPatch parses raw YAML and either deep-merges it into baseConfig
// (plain machine config patch) or appends it to standalonePatches (extension
// document that carries an apiVersion field, e.g. RegistryMirrorConfig).
func applyRawPatch(raw []byte, baseConfig map[string]interface{}, standalonePatches *[]string) (map[string]interface{}, error) {
	var p map[string]interface{}
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return baseConfig, fmt.Errorf("unmarshal patch: %w", err)
	}
	if _, isExtension := p["apiVersion"]; isExtension {
		*standalonePatches = append(*standalonePatches, strings.TrimSpace(string(raw)))
		return baseConfig, nil
	}
	return mergePatches(baseConfig, p), nil
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
		// Reconcile on spec changes (generation bump) AND on annotation changes so
		// that the talos.yuriykovalchuk.dev/reset=true annotation triggers a reconcile
		// immediately without waiting for the next drift-check requeue.
		WithEventFilter(predicate.Or[client.Object](
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
		)).
		Complete(r)
}
