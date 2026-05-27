package controller

import (
	"context"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// emitEvent records a Kubernetes event, safely ignoring a nil recorder.
// Replaces the identical event() method that was copied onto every reconciler.
func emitEvent(recorder record.EventRecorder, obj client.Object, eventType, reason, msg string) {
	if recorder != nil {
		recorder.Event(obj, eventType, reason, msg)
	}
}

// getSecret fetches a Secret by name and namespace.
// Callers are responsible for handling apierrors.IsNotFound on the returned error.
func getSecret(ctx context.Context, c client.Client, name, namespace string) (*corev1.Secret, error) {
	s := &corev1.Secret{}
	return s, c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, s)
}

// upsertSecret fetches the named secret and updates it, or creates it from scratch if absent.
// newFn builds the full Secret to Create; updateFn mutates an existing Secret before Update.
func upsertSecret(ctx context.Context, c client.Client, name, namespace string, newFn func() *corev1.Secret, updateFn func(*corev1.Secret)) error {
	existing, err := getSecret(ctx, c, name, namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = c.Create(ctx, newFn())
			appmetrics.SecretOperationsTotal.WithLabelValues("create", resultLabel(err)).Inc()
			return err
		}
		return err
	}
	updateFn(existing)
	err = c.Update(ctx, existing)
	appmetrics.SecretOperationsTotal.WithLabelValues("update", resultLabel(err)).Inc()
	return err
}

func resultLabel(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
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

// newSecret builds a bare Opaque Secret with a single data key.
func newSecret(name, namespace string, key string, data []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{key: data},
	}
}
