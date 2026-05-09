package controller

import (
	"context"

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
			return c.Create(ctx, newFn())
		}
		return err
	}
	updateFn(existing)
	return c.Update(ctx, existing)
}

// newSecret builds a bare Opaque Secret with a single data key.
func newSecret(name, namespace string, key string, data []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{key: data},
	}
}
