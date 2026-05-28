package controller

import (
	"context"
	"fmt"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// emitEvent records a Kubernetes event, safely ignoring a nil recorder.
// Replaces the identical event() method that was copied onto every reconciler.
func emitEvent(recorder record.EventRecorder, obj client.Object, eventType, reason, msg string) {
	if recorder != nil {
		recorder.Event(obj, eventType, reason, msg)
	}
}

// ensureFinalizer adds the cleanup finalizer and persists the object if it was absent.
// No-op (no API call) when the finalizer is already present.
func ensureFinalizer(ctx context.Context, c client.Client, obj client.Object) error {
	if talos.ContainsFinalizer(obj.GetFinalizers(), talos.FinalizerCleanup) {
		return nil
	}
	finalizers := obj.GetFinalizers()
	talos.AddFinalizer(&finalizers, talos.FinalizerCleanup)
	obj.SetFinalizers(finalizers)
	if err := c.Update(ctx, obj); err != nil {
		return fmt.Errorf("add finalizer: %w", err)
	}
	return nil
}

// getSecret fetches a Secret by name and namespace.
// Callers are responsible for handling apierrors.IsNotFound on the returned error.
func getSecret(ctx context.Context, c client.Client, name, namespace string) (*corev1.Secret, error) {
	s := &corev1.Secret{}
	return s, c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, s)
}

// getSecretOrSkip fetches a Secret and signals whether the caller should skip
// (when the secret is not found) rather than fail hard. Returns (nil, true, nil)
// on NotFound; (secret, false, nil) on success; (nil, false, err) on other errors.
func getSecretOrSkip(ctx context.Context, c client.Client, name, namespace string) (*corev1.Secret, bool, error) {
	s, err := getSecret(ctx, c, name, namespace)
	if apierrors.IsNotFound(err) {
		return nil, true, nil
	}
	return s, false, err
}

// upsertSecret fetches the named secret and updates it, or creates it from scratch if absent.
// newFn builds the full Secret to Create; updateFn mutates an existing Secret before Update.
// Retries on conflict so callers don't need to handle the resource version mismatch case.
func upsertSecret(ctx context.Context, c client.Client, name, namespace string, newFn func() *corev1.Secret, updateFn func(*corev1.Secret)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existing, err := getSecret(ctx, c, name, namespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				err = c.Create(ctx, newFn())
				appmetrics.SecretOperationsTotal.WithLabelValues("create", appmetrics.ResultLabel(err)).Inc()
				return err
			}
			return err
		}
		updateFn(existing)
		err = c.Update(ctx, existing)
		appmetrics.SecretOperationsTotal.WithLabelValues("update", appmetrics.ResultLabel(err)).Inc()
		return err
	})
}

// filterExclude returns a copy of items with all occurrences of exclude removed.
func filterExclude(items []string, exclude string) []string {
	var result []string
	for _, item := range items {
		if item != exclude {
			result = append(result, item)
		}
	}
	return result
}

// remoteClientOrFallback calls fn when non-nil (test injection), otherwise
// calls the real newRemoteClient. Both TalosNodeReconciler and
// TalosClusterBootstrapReconciler carry a NewRemoteClient field for this.
func remoteClientOrFallback(fn func([]byte) (kubernetes.Interface, error), kubeconfig []byte) (kubernetes.Interface, error) {
	if fn != nil {
		return fn(kubeconfig)
	}
	return newRemoteClient(kubeconfig)
}

// skipDrain returns true when node drain should be bypassed. Drain is skipped
// when spec.skipDrain is true OR when the escape-hatch annotation is present.
// The annotation is checked second so it can override a missing spec field on a
// terminating object without requiring a spec patch.
func skipDrain(node *v1alpha1.TalosNode) bool {
	return node.Spec.SkipDrain ||
		node.Annotations[talos.AnnotationSkipDrain] == "true"
}

// drainSkipReason returns a short human-readable string describing why drain
// is being skipped — used in log messages.
func drainSkipReason(node *v1alpha1.TalosNode) string {
	if node.Annotations[talos.AnnotationSkipDrain] == "true" {
		return "annotation " + talos.AnnotationSkipDrain
	}
	return "spec.skipDrain"
}

// patchAnnotations atomically sets and/or deletes annotations on node using a
// server-side MergeFrom patch.
// Pass nil for set when only deletions are needed; pass no del args when only additions are needed.
func patchAnnotations(ctx context.Context, c client.Client, node *v1alpha1.TalosNode, set map[string]string, del ...string) error {
	base := node.DeepCopy()

	if node.Annotations == nil {
		node.Annotations = make(map[string]string)
	}
	for k, v := range set {
		node.Annotations[k] = v
	}
	for _, k := range del {
		delete(node.Annotations, k)
	}
	return c.Patch(ctx, node, client.MergeFrom(base))
}

// secretKey returns the value for key from the secret's Data map, or an error
// when the key is absent or its value is empty. Use this instead of direct map
// access so that a miscreated or hand-edited Secret surfaces a clear error
// rather than silently applying an empty config.
func secretKey(s *corev1.Secret, key string) ([]byte, error) {
	v, ok := s.Data[key]
	if !ok || len(v) == 0 {
		return nil, fmt.Errorf("secret %s/%s missing or empty key %q", s.Namespace, s.Name, key)
	}
	return v, nil
}

// newSecret builds a bare Opaque Secret with a single data key.
func newSecret(name, namespace string, key string, data []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{key: data},
	}
}
