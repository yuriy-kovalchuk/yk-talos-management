package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/yuriy-kovalchuk/yk-talos-management/internal/config"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusterbootstraps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusterbootstraps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosnodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;delete

type TalosClusterBootstrapReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Talos    TalosDialer
	Recorder record.EventRecorder
}

func (r *TalosClusterBootstrapReconciler) event(obj client.Object, eventType, reason, msg string) {
	if r.Recorder != nil {
		r.Recorder.Event(obj, eventType, reason, msg)
	}
}

func (r *TalosClusterBootstrapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	var bootstrap v1alpha1.TalosClusterBootstrap

	if err := r.Get(ctx, req.NamespacedName, &bootstrap); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	l.Info("Reconciling TalosClusterBootstrap", "name", bootstrap.Name, "generation", bootstrap.Generation)

	if bootstrap.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &bootstrap)
	}

	talos.AddFinalizer(&bootstrap.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, &bootstrap); err != nil {
		return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
	}

	if bootstrap.Status.Phase == v1alpha1.TalosClusterBootstrapPhaseCompleted &&
		bootstrap.Status.ObservedGeneration == bootstrap.Generation {
		l.Info("Bootstrap complete, skipping", "generation", bootstrap.Generation)
		return ctrl.Result{}, nil
	}

	cluster := &v1alpha1.TalosCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: bootstrap.Spec.ClusterRef, Namespace: bootstrap.Namespace}, cluster); err != nil {
		return r.setError(ctx, &bootstrap, fmt.Errorf("get cluster: %w", err))
	}

	if len(cluster.Spec.Endpoints) == 0 {
		return r.setError(ctx, &bootstrap, fmt.Errorf("cluster has no endpoints"))
	}

	// Wait until at least one control plane node is Ready before touching the Talos API.
	// Without this guard, bootstrap fires while nodes are still applying their config
	// (maintenance-mode cert), causing a TLS verification failure that grows the
	// exponential backoff to minutes.
	if ready, err := r.readyControlPlaneCount(ctx, bootstrap.Namespace, bootstrap.Spec.ClusterRef); err != nil {
		return r.setError(ctx, &bootstrap, fmt.Errorf("list nodes: %w", err))
	} else if ready == 0 {
		bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseWaitingForNodes
		bootstrap.Status.Message = "Waiting for at least one control plane node to reach Ready phase"
		if err := r.Status().Update(ctx, &bootstrap); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Capture state before any mutations so idempotency checks reflect the
	// pre-reconcile truth, not the status we are about to write.
	alreadyBootstrapped := talos.HasCondition(bootstrap.Status.Conditions,
		v1alpha1.TalosClusterBootstrapConditionBootstrapped, metav1.ConditionTrue)

	bootstrap.Status.ObservedGeneration = bootstrap.Generation
	bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseBootstrapping
	if !alreadyBootstrapped {
		talos.SetCondition(&bootstrap.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.TalosClusterBootstrapConditionBootstrapped,
			Status:  metav1.ConditionFalse,
			Reason:  "Bootstrapping",
			Message: "Bootstrapping cluster",
		})
	}
	talos.SetCondition(&bootstrap.Status.Conditions, metav1.Condition{
		Type:    v1alpha1.TalosClusterBootstrapConditionKubeconfig,
		Status:  metav1.ConditionFalse,
		Reason:  "Retrieving",
		Message: "Retrieving kubeconfig",
	})
	if err := r.Status().Update(ctx, &bootstrap); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// Load talosconfig directly from the Kubernetes secret bytes — no temp file needed.
	talosconfigSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: cluster.Name + "-talosconfig", Namespace: bootstrap.Namespace}, talosconfigSecret); err != nil {
		return r.setError(ctx, &bootstrap, fmt.Errorf("get talosconfig secret: %w", err))
	}
	// Bootstrap must happen on endpoints[0] — calling it on another node creates a separate etcd
	// cluster. For kubeconfig retrieval (post-bootstrap) any control plane endpoint works, so we
	// try all of them in order.
	talosconfig := talosconfigSecret.Data["talosconfig"]
	var (
		conn     TalosConnection
		dialedTo string
		dialErr  error
	)
	if alreadyBootstrapped {
		conn, dialedTo, dialErr = r.dialAny(ctx, talosconfig, cluster.Spec.Endpoints)
	} else {
		dialedTo = cluster.Spec.Endpoints[0]
		conn, dialErr = r.Talos.Dial(ctx, talosconfig, dialedTo)
	}
	if dialErr != nil {
		return r.setError(ctx, &bootstrap, fmt.Errorf("create client: %w", dialErr))
	}
	defer conn.Close() //nolint:errcheck

	if !alreadyBootstrapped {
		if err := conn.Bootstrap(ctx, dialedTo); err != nil {
			return r.setError(ctx, &bootstrap, fmt.Errorf("bootstrap: %w", err))
		}
		talos.SetCondition(&bootstrap.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.TalosClusterBootstrapConditionBootstrapped,
			Status:  metav1.ConditionTrue,
			Reason:  "Bootstrapped",
			Message: "Cluster bootstrapped",
		})
		r.event(&bootstrap, corev1.EventTypeNormal, "Bootstrapped", "etcd bootstrap triggered successfully")
		if err := r.Status().Update(ctx, &bootstrap); err != nil {
			return ctrl.Result{}, fmt.Errorf("update status: %w", err)
		}
	}

	kubeconfig, err := conn.GetKubeconfig(ctx, dialedTo)
	if err != nil {
		l.Error(err, "get kubeconfig failed", "endpoint", dialedTo, "retry", bootstrap.Status.RetryCount+1)
		bootstrap.Status.RetryCount++
		bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseWaitingForKubeconfig
		talos.SetCondition(&bootstrap.Status.Conditions, metav1.Condition{
			Type:    v1alpha1.TalosClusterBootstrapConditionKubeconfig,
			Status:  metav1.ConditionFalse,
			Reason:  "Retrying",
			Message: fmt.Sprintf("Retrying (attempt %d)", bootstrap.Status.RetryCount),
		})
		if updateErr := r.Status().Update(ctx, &bootstrap); updateErr != nil {
			l.Error(updateErr, "update retry status")
		}
		return ctrl.Result{RequeueAfter: config.GetRetryDelay(bootstrap.Status.RetryCount)}, nil
	}

	if err := r.saveKubeconfig(ctx, cluster, kubeconfig); err != nil {
		log.FromContext(ctx).Error(err, "save kubeconfig")
	}

	bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseCompleted
	bootstrap.Status.Message = "Bootstrap completed"
	talos.SetCondition(&bootstrap.Status.Conditions, metav1.Condition{
		Type:    v1alpha1.TalosClusterBootstrapConditionKubeconfig,
		Status:  metav1.ConditionTrue,
		Reason:  "Retrieved",
		Message: "Kubeconfig retrieved",
	})
	if err := r.Status().Update(ctx, &bootstrap); err != nil {
		return ctrl.Result{}, fmt.Errorf("update final status: %w", err)
	}

	r.event(&bootstrap, corev1.EventTypeNormal, "Completed", "Bootstrap complete; kubeconfig stored")
	l.Info("Bootstrap complete", "endpoint", dialedTo)
	return ctrl.Result{}, nil
}

func (r *TalosClusterBootstrapReconciler) handleDeletion(ctx context.Context, bootstrap *v1alpha1.TalosClusterBootstrap) (ctrl.Result, error) {
	if !talos.ContainsFinalizer(bootstrap.Finalizers, talos.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}

	cluster := &v1alpha1.TalosCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: bootstrap.Spec.ClusterRef, Namespace: bootstrap.Namespace}, cluster); err == nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{Name: cluster.Name + "-kubeconfig", Namespace: cluster.Namespace}, secret); err == nil {
			_ = r.Delete(ctx, secret)
		}
	}

	bootstrap.Finalizers = talos.RemoveFinalizer(bootstrap.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, bootstrap); err != nil {
		return ctrl.Result{}, err
	}
	log.FromContext(ctx).Info("Bootstrap cleaned up", "name", bootstrap.Name)
	return ctrl.Result{}, nil
}

func (r *TalosClusterBootstrapReconciler) saveKubeconfig(ctx context.Context, cluster *v1alpha1.TalosCluster, kubeconfig []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name + "-kubeconfig",
			Namespace: cluster.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "talos.yuriykovalchuk.dev/v1alpha1",
				Kind:       "TalosCluster",
				Name:       cluster.Name,
				UID:        cluster.UID,
				Controller: ptr.To(true),
			}},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"kubeconfig": kubeconfig},
	}

	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, secret)
		}
		return err
	}
	existing.Data["kubeconfig"] = kubeconfig
	return r.Update(ctx, &existing)
}

func (r *TalosClusterBootstrapReconciler) setError(ctx context.Context, bootstrap *v1alpha1.TalosClusterBootstrap, err error) (ctrl.Result, error) {
	bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseError
	bootstrap.Status.Message = err.Error()
	r.event(bootstrap, corev1.EventTypeWarning, "Failed", err.Error())
	if updateErr := r.Status().Update(ctx, bootstrap); updateErr != nil {
		log.FromContext(ctx).Error(updateErr, "update error status")
	}
	return ctrl.Result{}, err
}

// readyControlPlaneCount returns the number of ControlPlane TalosNodes for the
// given clusterRef that are in the Ready phase.
func (r *TalosClusterBootstrapReconciler) readyControlPlaneCount(ctx context.Context, namespace, clusterRef string) (int, error) {
	var nodes v1alpha1.TalosNodeList
	if err := r.List(ctx, &nodes, client.InNamespace(namespace)); err != nil {
		return 0, err
	}
	count := 0
	for _, n := range nodes.Items {
		if n.Spec.ClusterRef == clusterRef &&
			n.Spec.Role == v1alpha1.TalosNodeRoleControlPlane &&
			n.Status.Phase == v1alpha1.TalosNodePhaseReady {
			count++
		}
	}
	return count, nil
}

// nodeToBootstrap maps a TalosNode to the TalosClusterBootstrap(s) that reference the same cluster.
func (r *TalosClusterBootstrapReconciler) nodeToBootstrap(ctx context.Context, obj client.Object) []reconcile.Request {
	node, ok := obj.(*v1alpha1.TalosNode)
	if !ok || node.Spec.Role != v1alpha1.TalosNodeRoleControlPlane {
		return nil
	}
	var list v1alpha1.TalosClusterBootstrapList
	if err := r.List(ctx, &list, client.InNamespace(node.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, b := range list.Items {
		if b.Spec.ClusterRef == node.Spec.ClusterRef {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: b.Name, Namespace: b.Namespace},
			})
		}
	}
	return reqs
}

// nodeReadyPredicate fires only when a ControlPlane TalosNode transitions into the Ready phase.
func nodeReadyPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldNode, ok1 := e.ObjectOld.(*v1alpha1.TalosNode)
			newNode, ok2 := e.ObjectNew.(*v1alpha1.TalosNode)
			if !ok1 || !ok2 {
				return false
			}
			return newNode.Spec.Role == v1alpha1.TalosNodeRoleControlPlane &&
				oldNode.Status.Phase != v1alpha1.TalosNodePhaseReady &&
				newNode.Status.Phase == v1alpha1.TalosNodePhaseReady
		},
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// dialAny tries each endpoint in order and returns the first successful connection.
// Used for operations that can target any available control plane (e.g. GetKubeconfig).
func (r *TalosClusterBootstrapReconciler) dialAny(ctx context.Context, talosconfig []byte, endpoints []string) (TalosConnection, string, error) {
	var lastErr error
	for _, ep := range endpoints {
		conn, err := r.Talos.Dial(ctx, talosconfig, ep)
		if err == nil {
			return conn, ep, nil
		}
		lastErr = fmt.Errorf("dial %s: %w", ep, err)
	}
	return nil, "", lastErr
}

func (r *TalosClusterBootstrapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TalosClusterBootstrap{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.TalosNode{},
			handler.EnqueueRequestsFromMapFunc(r.nodeToBootstrap),
			builder.WithPredicates(nodeReadyPredicate())).
		Complete(r)
}
