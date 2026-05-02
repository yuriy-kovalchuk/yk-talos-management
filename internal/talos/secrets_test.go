package talos

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSecretManager(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	t.Run("Create creates secret when not exists", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		sm := NewSecretManager(c, scheme, "owner", "uid-1")

		err := sm.Create(context.Background(), "my-secret", "default", "key", "value", corev1.SecretTypeOpaque)

		if err != nil {
			t.Errorf("Create() error = %v", err)
		}

		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: "my-secret", Namespace: "default"}, secret); err != nil {
			t.Errorf("expected secret to exist")
		}
		if string(secret.Data["key"]) != "value" {
			t.Errorf("expected secret data 'key' = 'value', got %s", string(secret.Data["key"]))
		}
	})

	t.Run("Create skips already exists", func(t *testing.T) {
		existing := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "default"},
			Data:       map[string][]byte{"key": []byte("old")},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
		sm := NewSecretManager(c, scheme, "owner", "uid-1")

		err := sm.Create(context.Background(), "existing", "default", "key", "new", corev1.SecretTypeOpaque)

		if err != nil {
			t.Errorf("Create() error = %v", err)
		}

		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: "existing", Namespace: "default"}, secret); err != nil {
			t.Fatalf("expected secret to still exist: %v", err)
		}
		if string(secret.Data["key"]) != "old" {
			t.Errorf("Create() overwrote existing data: got %q, want %q", string(secret.Data["key"]), "old")
		}
	})

	t.Run("CreateOrUpdate creates new secret", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		sm := NewSecretManager(c, scheme, "owner", "uid-1")

		err := sm.CreateOrUpdate(context.Background(), "new-secret", "default", "key", "value", corev1.SecretTypeOpaque)

		if err != nil {
			t.Errorf("CreateOrUpdate() error = %v", err)
		}

		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: "new-secret", Namespace: "default"}, secret); err != nil {
			t.Errorf("expected secret to exist")
		}
	})

	t.Run("CreateOrUpdate updates existing secret", func(t *testing.T) {
		existing := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "default"},
			Data:       map[string][]byte{"key": []byte("old")},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
		sm := NewSecretManager(c, scheme, "owner", "uid-1")

		err := sm.CreateOrUpdate(context.Background(), "existing", "default", "key", "updated", corev1.SecretTypeOpaque)

		if err != nil {
			t.Errorf("CreateOrUpdate() error = %v", err)
		}

		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: "existing", Namespace: "default"}, secret); err != nil {
			t.Errorf("expected secret to exist")
		}
		if string(secret.Data["key"]) != "updated" {
			t.Errorf("expected 'updated', got %s", string(secret.Data["key"]))
		}
	})

	t.Run("Delete removes secret", func(t *testing.T) {
		existing := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "to-delete", Namespace: "default"},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
		sm := NewSecretManager(c, scheme, "owner", "uid-1")

		err := sm.Delete(context.Background(), "to-delete", "default")

		if err != nil {
			t.Errorf("Delete() error = %v", err)
		}

		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Name: "to-delete", Namespace: "default"}, secret); err == nil {
			t.Errorf("expected secret to be deleted")
		} else if !apierrors.IsNotFound(err) {
			t.Errorf("expected NotFound error, got %v", err)
		}
	})

	t.Run("Delete returns nil when not found", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		sm := NewSecretManager(c, scheme, "owner", "uid-1")

		err := sm.Delete(context.Background(), "nonexistent", "default")

		if err != nil {
			t.Errorf("Delete() error = %v", err)
		}
	})

	t.Run("DeleteMultiple deletes all", func(t *testing.T) {
		secrets := []client.Object{
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret-1", Namespace: "default"}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret-2", Namespace: "default"}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret-3", Namespace: "default"}},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secrets...).Build()
		sm := NewSecretManager(c, scheme, "owner", "uid-1")

		err := sm.DeleteMultiple(context.Background(), "default", "secret-1", "secret-2", "secret-3")

		if err != nil {
			t.Errorf("DeleteMultiple() error = %v", err)
		}

		for _, name := range []string{"secret-1", "secret-2", "secret-3"} {
			secret := &corev1.Secret{}
			if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, secret); err == nil {
				t.Errorf("expected %s to be deleted", name)
			}
		}
	})

	t.Run("sets owner reference", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		sm := NewSecretManager(c, scheme, "my-cluster", "uid-123")

		err := sm.Create(context.Background(), "test-secret", "default", "key", "value", corev1.SecretTypeOpaque)

		if err != nil {
			t.Errorf("Create() error = %v", err)
		}

		secret := &corev1.Secret{}
		c.Get(context.Background(), types.NamespacedName{Name: "test-secret", Namespace: "default"}, secret)

		if len(secret.OwnerReferences) != 1 {
			t.Errorf("expected 1 owner reference, got %d", len(secret.OwnerReferences))
		}
		if secret.OwnerReferences[0].Name != "my-cluster" {
			t.Errorf("expected owner name 'my-cluster', got %s", secret.OwnerReferences[0].Name)
		}
		if secret.OwnerReferences[0].Controller != nil && !*secret.OwnerReferences[0].Controller {
			t.Error("expected Controller = true")
		}
	})
}