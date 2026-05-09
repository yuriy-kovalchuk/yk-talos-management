package controller

import (
	"context"
	"fmt"
	"strings"

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

func (r *TalosNodeReconciler) event(obj client.Object, eventType, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(obj, eventType, reason, msg)
	}
}

func (r *TalosNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	var node v1alpha1.TalosNode

	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	l.Info("Reconciling TalosNode", "name", node.Name, "ip", node.Spec.NodeIP, "generation", node.Generation)

	if node.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &node)
	}

	talos.AddFinalizer(&node.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, &node); err != nil {
		return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
	}

	if isNodeUpToDate(&node) {
		l.Info("Node up-to-date, skipping", "generation", node.Generation)
		return ctrl.Result{}, nil
	}

	if err := r.applyConfig(ctx, &node); err != nil {
		if talos.IsContextCancelled(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		node.Status.Phase = v1alpha1.TalosNodePhaseError
		node.Status.RetryCount++
		node.Status.Message = err.Error()
		r.event(&node, corev1.EventTypeWarning, "ApplyFailed", err.Error())
		if updateErr := r.Status().Update(ctx, &node); updateErr != nil {
			l.Error(updateErr, "update error status")
		}
		return ctrl.Result{RequeueAfter: config.GetRetryDelay(node.Status.RetryCount)}, nil
	}

	l.Info("Node configured", "ip", node.Spec.NodeIP)
	r.event(&node, corev1.EventTypeNormal, "Applied", "Machine configuration applied successfully")
	return ctrl.Result{}, nil
}

// isNodeUpToDate returns true when the node config has been successfully applied and the spec hasn't changed.
func isNodeUpToDate(node *v1alpha1.TalosNode) bool {
	return node.Status.ObservedGeneration == node.Generation &&
		node.Status.Phase == v1alpha1.TalosNodePhaseReady &&
		talos.HasCondition(node.Status.Conditions, v1alpha1.TalosNodeConditionConfigApplied, metav1.ConditionTrue)
}

func (r *TalosNodeReconciler) handleDeletion(ctx context.Context, node *v1alpha1.TalosNode) (ctrl.Result, error) {
	if !talos.ContainsFinalizer(node.Finalizers, talos.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: node.Name + "-config", Namespace: node.Namespace}, secret); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	} else if err := r.Delete(ctx, secret); err != nil {
		return ctrl.Result{}, err
	}

	node.Finalizers = talos.RemoveFinalizer(node.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, node); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("Node cleaned up", "name", node.Name)
	return ctrl.Result{}, nil
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

	secretName, key := cluster.Name+"-controlplane", "controlplane.yaml"
	if node.Spec.Role == v1alpha1.TalosNodeRoleWorker {
		secretName, key = cluster.Name+"-worker", "worker.yaml"
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: node.Namespace}, secret); err != nil {
		return fmt.Errorf("get config secret: %w", err)
	}

	var baseConfig map[string]interface{}
	if err := yaml.Unmarshal(secret.Data[key], &baseConfig); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}

	var standalonePatches []string
	for _, patch := range node.Spec.Patches {
		var p map[string]interface{}
		if err := yaml.Unmarshal([]byte(patch), &p); err != nil {
			return fmt.Errorf("unmarshal patch: %w", err)
		}
		if _, ok := p["machine"]; ok {
			baseConfig = mergePatches(baseConfig, p)
		} else {
			standalonePatches = append(standalonePatches, strings.TrimSpace(patch))
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
		talosconfigSecret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: cluster.Name + "-talosconfig", Namespace: node.Namespace}, talosconfigSecret); err != nil {
			return fmt.Errorf("get talosconfig secret: %w", err)
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
	talos.SetCondition(&node.Status.Conditions, metav1.Condition{
		Type:    v1alpha1.TalosNodeConditionConfigApplied,
		Status:  metav1.ConditionTrue,
		Reason:  "Applied",
		Message: "Configuration applied successfully",
	})
	now := metav1.Now()
	node.Status.LastUpdateTime = &now
	return r.Status().Update(ctx, node)
}

// saveNodeConfig persists the final merged machine config (base + patches) to a secret so it
// can be inspected for debugging and used for drift detection in the future.
func (r *TalosNodeReconciler) saveNodeConfig(ctx context.Context, node *v1alpha1.TalosNode, configBytes []byte) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: node.Name + "-config", Namespace: node.Namespace}, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      node.Name + "-config",
				Namespace: node.Namespace,
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"config.yaml": configBytes},
		}
		return r.Create(ctx, secret)
	}
	secret.Data = map[string][]byte{"config.yaml": configBytes}
	return r.Update(ctx, secret)
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
