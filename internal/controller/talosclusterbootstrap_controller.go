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
	"k8s.io/client-go/kubernetes"
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
	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusterbootstraps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusterbootstraps/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosnodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;delete

type TalosClusterBootstrapReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Talos           TalosDialer
	Recorder        record.EventRecorder
	// NewRemoteClient builds a Kubernetes client from admin kubeconfig bytes.
	// Injected in tests; production falls back to newRemoteClient (in talosnode_drain.go).
	NewRemoteClient func(kubeconfig []byte) (kubernetes.Interface, error)
}

func (r *TalosClusterBootstrapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	var bootstrap v1alpha1.TalosClusterBootstrap

	if err := r.Get(ctx, req.NamespacedName, &bootstrap); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	l.V(1).Info("reconciling TalosClusterBootstrap", "generation", bootstrap.Generation)
	start := time.Now()
	defer func() { l.V(1).Info("reconcile done", "duration", time.Since(start)) }()
	appmetrics.RecordBootstrapPhase(bootstrap.Spec.ClusterRef, bootstrap.Namespace, string(bootstrap.Status.Phase), string(bootstrap.Status.Phase))

	if bootstrap.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &bootstrap)
	}

	if err := ensureFinalizer(ctx, r.Client, &bootstrap); err != nil {
		return ctrl.Result{}, err
	}

	if isBootstrapUpToDate(&bootstrap) {
		l.V(1).Info("bootstrap complete, skipping", "generation", bootstrap.Generation)
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
	if result, ready, err := r.waitForReadyNodes(ctx, &bootstrap); err != nil {
		return r.setError(ctx, &bootstrap, err)
	} else if !ready {
		return result, nil
	}

	// Short-circuit: kubeconfig is already saved — skip re-dialing Talos and go
	// straight to the API server readiness probe. This is the re-entry path from
	// WaitingForAPIServer as well as any other requeue after kubeconfig is stored.
	if talos.HasCondition(bootstrap.Status.Conditions,
		v1alpha1.TalosClusterBootstrapConditionKubeconfig, metav1.ConditionTrue) {
		return r.waitForAPIServer(ctx, &bootstrap, cluster)
	}

	// Capture state before any mutations so idempotency checks reflect the
	// pre-reconcile truth, not the status we are about to write.
	alreadyBootstrapped := talos.HasCondition(bootstrap.Status.Conditions,
		v1alpha1.TalosClusterBootstrapConditionBootstrapped, metav1.ConditionTrue)

	bootstrap.Status.ObservedGeneration = bootstrap.Generation
	appmetrics.RecordBootstrapPhase(bootstrap.Spec.ClusterRef, bootstrap.Namespace, string(bootstrap.Status.Phase), string(v1alpha1.TalosClusterBootstrapPhaseBootstrapping))
	bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseBootstrapping
	if !alreadyBootstrapped {
		talos.SetConditionStatus(&bootstrap.Status.Conditions,
			v1alpha1.TalosClusterBootstrapConditionBootstrapped, metav1.ConditionFalse,
			"Bootstrapping", "Bootstrapping cluster")
	}
	talos.SetConditionStatus(&bootstrap.Status.Conditions,
		v1alpha1.TalosClusterBootstrapConditionKubeconfig, metav1.ConditionFalse,
		"Retrieving", "Retrieving kubeconfig")
	if err := r.Status().Update(ctx, &bootstrap); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// Load talosconfig directly from the Kubernetes secret bytes — no temp file needed.
	talosconfigSecret, err := getSecret(ctx, r.Client, clusterTalosconfigName(cluster.Name), bootstrap.Namespace)
	if err != nil {
		return r.setError(ctx, &bootstrap, fmt.Errorf("get talosconfig secret: %w", err))
	}
	// Bootstrap must happen on endpoints[0] — calling it on another node creates a separate etcd
	// cluster. For kubeconfig retrieval (post-bootstrap) any control plane endpoint works, so we
	// try all of them in order.
	talosconfig, err := secretKey(talosconfigSecret, "talosconfig")
	if err != nil {
		return r.setError(ctx, &bootstrap, fmt.Errorf("read talosconfig: %w", err))
	}
	var (
		conn     TalosConnection
		dialedTo string
		dialErr  error
	)
	if alreadyBootstrapped {
		conn, dialedTo, dialErr = dialAny(ctx, r.Talos, talosconfig, cluster.Spec.Endpoints)
	} else {
		dialedTo = cluster.Spec.Endpoints[0]
		conn, dialErr = r.Talos.Dial(ctx, talosconfig, dialedTo)
	}
	if dialErr != nil {
		if talos.IsContextCancelled(dialErr) {
			return ctrl.Result{}, nil
		}
		return r.setError(ctx, &bootstrap, fmt.Errorf("create client: %w", dialErr))
	}
	defer conn.Close() //nolint:errcheck

	if !alreadyBootstrapped {
		if err := conn.Bootstrap(ctx, dialedTo); err != nil {
			if talos.IsContextCancelled(err) {
				return ctrl.Result{}, nil
			}
			return r.setError(ctx, &bootstrap, fmt.Errorf("bootstrap: %w", err))
		}
		talos.SetConditionStatus(&bootstrap.Status.Conditions,
			v1alpha1.TalosClusterBootstrapConditionBootstrapped, metav1.ConditionTrue,
			"Bootstrapped", "Cluster bootstrapped")
		emitEvent(r.Recorder, &bootstrap, corev1.EventTypeNormal, "Bootstrapped", "etcd bootstrap triggered successfully")
		if err := r.Status().Update(ctx, &bootstrap); err != nil {
			return ctrl.Result{}, fmt.Errorf("update bootstrapped status: %w", err)
		}
	}

	kubeconfig, err := conn.GetKubeconfig(ctx, dialedTo)
	if err != nil {
		if talos.IsContextCancelled(err) {
			return ctrl.Result{}, nil
		}
		bootstrap.Status.RetryCount++
		delay := config.GetRetryDelay(bootstrap.Status.RetryCount)
		l.Error(err, "get kubeconfig failed", "endpoint", dialedTo, "attempt", bootstrap.Status.RetryCount, "requeueAfter", delay.String())
		appmetrics.RecordBootstrapPhase(bootstrap.Spec.ClusterRef, bootstrap.Namespace, string(v1alpha1.TalosClusterBootstrapPhaseBootstrapping), string(v1alpha1.TalosClusterBootstrapPhaseWaitingForKubeconfig))
		bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseWaitingForKubeconfig
		talos.SetConditionStatus(&bootstrap.Status.Conditions,
			v1alpha1.TalosClusterBootstrapConditionKubeconfig, metav1.ConditionFalse,
			"Retrying", fmt.Sprintf("Retrying (attempt %d)", bootstrap.Status.RetryCount))
		if updateErr := r.Status().Update(ctx, &bootstrap); updateErr != nil {
			l.Error(updateErr, "update retry status")
		}
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	if err := r.saveKubeconfig(ctx, cluster, kubeconfig); err != nil {
		return r.setError(ctx, &bootstrap, fmt.Errorf("save kubeconfig: %w", err))
	}

	// Persist KubeconfigAvailable=True so the short-circuit path picks it up on
	// any subsequent requeue (WaitingForAPIServer, controller restart, etc.).
	talos.SetConditionStatus(&bootstrap.Status.Conditions,
		v1alpha1.TalosClusterBootstrapConditionKubeconfig, metav1.ConditionTrue,
		"Retrieved", "Kubeconfig retrieved")
	if err := r.Status().Update(ctx, &bootstrap); err != nil {
		return ctrl.Result{}, fmt.Errorf("update kubeconfig status: %w", err)
	}

	return r.waitForAPIServer(ctx, &bootstrap, cluster)
}

// waitForAPIServer loads the saved kubeconfig and probes the Kubernetes API server.
// Returns Completed when reachable, or requeues with WaitingForAPIServer until it is.
// This separates the "kubeconfig bytes exist" signal from "cluster is actually usable".
func (r *TalosClusterBootstrapReconciler) waitForAPIServer(ctx context.Context, bootstrap *v1alpha1.TalosClusterBootstrap, cluster *v1alpha1.TalosCluster) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	kubeconfigSecret, err := getSecret(ctx, r.Client, clusterKubeconfigName(cluster.Name), cluster.Namespace)
	if err != nil {
		return r.setError(ctx, bootstrap, fmt.Errorf("load kubeconfig secret: %w", err))
	}

	kubeconfigBytes, apiErr := secretKey(kubeconfigSecret, "kubeconfig")
	var apiClient kubernetes.Interface
	if apiErr == nil {
		apiClient, apiErr = remoteClientOrFallback(r.NewRemoteClient, kubeconfigBytes)
	}
	if apiErr == nil {
		_, apiErr = apiClient.Discovery().ServerVersion()
	}
	if apiErr != nil {
		l.Info("kubernetes API server not yet reachable, will retry",
			"requeueAfter", apiServerCheckDelay.String(), "err", apiErr)
		appmetrics.RecordBootstrapPhase(bootstrap.Spec.ClusterRef, bootstrap.Namespace,
			string(bootstrap.Status.Phase), string(v1alpha1.TalosClusterBootstrapPhaseWaitingForAPIServer))
		bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseWaitingForAPIServer
		bootstrap.Status.Message = "Waiting for Kubernetes API server to become reachable"
		talos.SetConditionStatus(&bootstrap.Status.Conditions,
			v1alpha1.TalosClusterBootstrapConditionAPIServer, metav1.ConditionFalse,
			"NotReady", fmt.Sprintf("API server unreachable: %v", apiErr))
		now := metav1.Now()
		bootstrap.Status.LastUpdateTime = &now
		if updateErr := r.Status().Update(ctx, bootstrap); updateErr != nil {
			l.Error(updateErr, "update api server wait status")
		}
		return ctrl.Result{RequeueAfter: apiServerCheckDelay}, nil
	}

	appmetrics.RecordBootstrapPhase(bootstrap.Spec.ClusterRef, bootstrap.Namespace,
		string(bootstrap.Status.Phase), string(v1alpha1.TalosClusterBootstrapPhaseCompleted))
	bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseCompleted
	bootstrap.Status.RetryCount = 0
	bootstrap.Status.Message = "Bootstrap completed"
	talos.SetConditionStatus(&bootstrap.Status.Conditions,
		v1alpha1.TalosClusterBootstrapConditionAPIServer, metav1.ConditionTrue,
		"Ready", "Kubernetes API server is reachable")
	completedAt := metav1.Now()
	bootstrap.Status.LastUpdateTime = &completedAt
	if err := r.Status().Update(ctx, bootstrap); err != nil {
		return ctrl.Result{}, fmt.Errorf("update completed status: %w", err)
	}

	appmetrics.BootstrapDuration.WithLabelValues(bootstrap.Spec.ClusterRef).Observe(
		time.Since(bootstrap.CreationTimestamp.Time).Seconds())
	emitEvent(r.Recorder, bootstrap, corev1.EventTypeNormal, "Completed",
		"Bootstrap complete; kubeconfig stored and API server reachable")
	l.Info("bootstrap complete")
	return ctrl.Result{}, nil
}

// isBootstrapUpToDate returns true when bootstrap has completed, the spec is unchanged,
// and the API server readiness condition is confirmed. Checking the condition prevents
// a controller restart from skipping the API-server probe when the phase was set to
// Completed without the condition ever being written (e.g. partial failure).
func isBootstrapUpToDate(b *v1alpha1.TalosClusterBootstrap) bool {
	return b.Status.Phase == v1alpha1.TalosClusterBootstrapPhaseCompleted &&
		b.Status.ObservedGeneration == b.Generation &&
		talos.HasCondition(b.Status.Conditions, v1alpha1.TalosClusterBootstrapConditionAPIServer, metav1.ConditionTrue)
}

// waitForReadyNodes checks whether at least one ControlPlane node for this bootstrap's
// cluster is Ready. Returns (result, false, nil) to signal the caller should requeue,
// or (_, true, nil) to signal reconciliation can proceed.
func (r *TalosClusterBootstrapReconciler) waitForReadyNodes(ctx context.Context, bootstrap *v1alpha1.TalosClusterBootstrap) (ctrl.Result, bool, error) {
	ready, err := r.readyControlPlaneCount(ctx, bootstrap.Namespace, bootstrap.Spec.ClusterRef)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("list nodes: %w", err)
	}
	if ready > 0 {
		return ctrl.Result{}, true, nil
	}
	appmetrics.RecordBootstrapPhase(bootstrap.Spec.ClusterRef, bootstrap.Namespace, string(bootstrap.Status.Phase), string(v1alpha1.TalosClusterBootstrapPhaseWaitingForNodes))
	bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseWaitingForNodes
	bootstrap.Status.Message = "Waiting for at least one control plane node to reach Ready phase"
	if err := r.Status().Update(ctx, bootstrap); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("update status: %w", err)
	}
	log.FromContext(ctx).Info("waiting for ready control plane node", "requeueAfter", nodeReadyDelay.String())
	return ctrl.Result{RequeueAfter: nodeReadyDelay}, false, nil
}

func (r *TalosClusterBootstrapReconciler) handleDeletion(ctx context.Context, bootstrap *v1alpha1.TalosClusterBootstrap) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	if !talos.ContainsFinalizer(bootstrap.Finalizers, talos.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}

	cluster := &v1alpha1.TalosCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: bootstrap.Spec.ClusterRef, Namespace: bootstrap.Namespace}, cluster); err != nil {
		if !apierrors.IsNotFound(err) {
			l.Error(err, "get cluster for kubeconfig cleanup", "cluster", bootstrap.Spec.ClusterRef)
		}
	} else {
		secret, err := getSecret(ctx, r.Client, clusterKubeconfigName(cluster.Name), cluster.Namespace)
		if err == nil {
			if delErr := r.Delete(ctx, secret); delErr != nil && !apierrors.IsNotFound(delErr) {
				return ctrl.Result{}, delErr
			}
		}
	}

	bootstrap.Finalizers = talos.RemoveFinalizer(bootstrap.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, bootstrap); err != nil {
		return ctrl.Result{}, err
	}
	l.Info("bootstrap cleaned up")
	return ctrl.Result{}, nil
}

func (r *TalosClusterBootstrapReconciler) saveKubeconfig(ctx context.Context, cluster *v1alpha1.TalosCluster, kubeconfig []byte) error {
	name := clusterKubeconfigName(cluster.Name)
	newFn := func() *corev1.Secret {
		s := newSecret(name, cluster.Namespace, "kubeconfig", kubeconfig)
		s.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "talos.yuriykovalchuk.dev/v1alpha1",
			Kind:       "TalosCluster",
			Name:       cluster.Name,
			UID:        cluster.UID,
			Controller: ptr.To(true),
		}}
		return s
	}
	return upsertSecret(ctx, r.Client, name, cluster.Namespace,
		newFn,
		func(s *corev1.Secret) { s.Data["kubeconfig"] = kubeconfig },
	)
}

func (r *TalosClusterBootstrapReconciler) setError(ctx context.Context, bootstrap *v1alpha1.TalosClusterBootstrap, err error) (ctrl.Result, error) {
	bootstrap.Status.RetryCount++
	delay := config.GetRetryDelay(bootstrap.Status.RetryCount)
	appmetrics.RecordBootstrapPhase(bootstrap.Spec.ClusterRef, bootstrap.Namespace, string(bootstrap.Status.Phase), string(v1alpha1.TalosClusterBootstrapPhaseError))
	bootstrap.Status.Phase = v1alpha1.TalosClusterBootstrapPhaseError
	bootstrap.Status.Message = err.Error()
	emitEvent(r.Recorder, bootstrap, corev1.EventTypeWarning, "Failed", err.Error())
	if updateErr := r.Status().Update(ctx, bootstrap); updateErr != nil {
		log.FromContext(ctx).Error(updateErr, "update error status")
	}
	return ctrl.Result{RequeueAfter: delay}, nil
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

func (r *TalosClusterBootstrapReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TalosClusterBootstrap{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.TalosNode{},
			handler.EnqueueRequestsFromMapFunc(r.nodeToBootstrap),
			builder.WithPredicates(nodeReadyPredicate())).
		Complete(r)
}
