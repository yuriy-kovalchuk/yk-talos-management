package controller

import (
	"context"
	"fmt"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=talos.yuriykovalchuk.dev,resources=talosclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=create;get;list;update;watch

type TalosClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *TalosClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	var cluster v1alpha1.TalosCluster

	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	l.Info("Reconciling TalosCluster", "name", cluster.Name, "generation", cluster.Generation)

	if cluster.DeletionTimestamp != nil {
		return r.handleDeletion(ctx, &cluster)
	}

	talos.AddFinalizer(&cluster.Finalizers, talos.FinalizerCleanup)
	if err := r.Update(ctx, &cluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
	}

	if isUpToDate(&cluster) {
		l.Info("Cluster up-to-date, skipping", "generation", cluster.Generation)
		return ctrl.Result{}, nil
	}

	if err := r.provision(ctx, &cluster); err != nil {
		if talos.IsContextCancelled(err) {
			return ctrl.Result{}, nil
		}
		cluster.Status.Phase = v1alpha1.TalosPhaseError
		emitEvent(r.Recorder, &cluster, corev1.EventTypeWarning, "ProvisionFailed", err.Error())
		if updateErr := r.Status().Update(ctx, &cluster); updateErr != nil {
			l.Error(updateErr, "failed to update error status")
		}
		return ctrl.Result{}, fmt.Errorf("provision: %w", err)
	}

	l.Info("Cluster provisioned", "phase", cluster.Status.Phase)
	emitEvent(r.Recorder, &cluster, corev1.EventTypeNormal, "Provisioned", "Cluster configs and secrets generated successfully")
	return ctrl.Result{}, nil
}

func (r *TalosClusterReconciler) handleDeletion(ctx context.Context, cluster *v1alpha1.TalosCluster) (ctrl.Result, error) {
	if !talos.ContainsFinalizer(cluster.Finalizers, talos.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}

	log.FromContext(ctx).Info("Cleaning up cluster resources", "name", cluster.Name)

	sm := talos.NewSecretManager(r.Client, r.Scheme, cluster.Name, cluster.UID)
	if err := sm.DeleteMultiple(ctx, cluster.Namespace,
		clusterSecretsName(cluster.Name),
		clusterControlPlaneName(cluster.Name),
		clusterWorkerName(cluster.Name),
		clusterTalosconfigName(cluster.Name),
	); err != nil {
		return ctrl.Result{}, err
	}

	cluster.Finalizers = talos.RemoveFinalizer(cluster.Finalizers, talos.FinalizerCleanup)
	return ctrl.Result{}, r.Update(ctx, cluster)
}

// isUpToDate returns true when the cluster is already fully provisioned and the spec hasn't changed.
func isUpToDate(cluster *v1alpha1.TalosCluster) bool {
	return cluster.Status.ObservedGeneration == cluster.Generation &&
		cluster.Status.Phase == v1alpha1.TalosPhaseReady &&
		talos.HasCondition(cluster.Status.Conditions, v1alpha1.TalosClusterConditionConfigsGenerated, metav1.ConditionTrue)
}

func (r *TalosClusterReconciler) provision(ctx context.Context, cluster *v1alpha1.TalosCluster) error {
	cluster.Status.ObservedGeneration = cluster.Generation
	cluster.Status.Phase = v1alpha1.TalosPhaseProvisioning
	talos.SetConditionStatus(&cluster.Status.Conditions,
		v1alpha1.TalosClusterConditionSecretsGenerated, metav1.ConditionFalse, "Generating", "Generating cluster secrets")
	talos.SetConditionStatus(&cluster.Status.Conditions,
		v1alpha1.TalosClusterConditionConfigsGenerated, metav1.ConditionFalse, "Generating", "Generating cluster configs")
	if err := r.Status().Update(ctx, cluster); err != nil {
		return fmt.Errorf("update status: %w", err)
	}

	if len(cluster.Spec.Endpoints) == 0 {
		return fmt.Errorf("no endpoints configured")
	}

	// Load the existing bundle if the secrets secret was already created in a prior
	// (possibly failed) reconcile, so that all config secrets always share the same CA.
	var existingBundleJSON []byte
	if existingSecrets, err := getSecret(ctx, r.Client, clusterSecretsName(cluster.Name), cluster.Namespace); err == nil {
		existingBundleJSON = existingSecrets.Data["secrets.yaml"]
	}
	bundle, bundleBytes, err := talos.LoadOrGenSecretsBundle(existingBundleJSON, cluster.Spec.TalosVersion)
	if err != nil {
		return err
	}

	sm := talos.NewSecretManager(r.Client, r.Scheme, cluster.Name, cluster.UID)
	if err := sm.Create(ctx, clusterSecretsName(cluster.Name), cluster.Namespace,
		"secrets.yaml", string(bundleBytes), corev1.SecretTypeOpaque); err != nil {
		return fmt.Errorf("store secrets: %w", err)
	}

	configs, err := talos.GenConfig(cluster.Spec.ClusterName, cluster.Spec.Endpoints, cluster.Spec.TalosVersion, bundle)
	if err != nil {
		return err
	}

	if err := sm.CreateOrUpdate(ctx, clusterControlPlaneName(cluster.Name), cluster.Namespace,
		"controlplane.yaml", string(configs.ControlPlane), corev1.SecretTypeOpaque); err != nil {
		return fmt.Errorf("store controlplane: %w", err)
	}
	if err := sm.CreateOrUpdate(ctx, clusterWorkerName(cluster.Name), cluster.Namespace,
		"worker.yaml", string(configs.Worker), corev1.SecretTypeOpaque); err != nil {
		return fmt.Errorf("store worker: %w", err)
	}
	if err := sm.CreateOrUpdate(ctx, clusterTalosconfigName(cluster.Name), cluster.Namespace,
		"talosconfig", string(configs.Talosconfig), corev1.SecretTypeOpaque); err != nil {
		return fmt.Errorf("store talosconfig: %w", err)
	}

	cluster.Status.Phase = v1alpha1.TalosPhaseReady
	now := metav1.Now()
	cluster.Status.LastUpdateTime = &now
	talos.SetConditionStatus(&cluster.Status.Conditions,
		v1alpha1.TalosClusterConditionSecretsGenerated, metav1.ConditionTrue, "Generated", "Cluster secrets generated successfully")
	talos.SetConditionStatus(&cluster.Status.Conditions,
		v1alpha1.TalosClusterConditionConfigsGenerated, metav1.ConditionTrue, "Generated", "Cluster configs generated successfully")
	return r.Status().Update(ctx, cluster)
}

func (r *TalosClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.TalosCluster{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
