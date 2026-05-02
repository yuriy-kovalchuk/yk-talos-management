package talos

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SecretManager struct {
	Client client.Client
	Scheme *runtime.Scheme
	Owner  metav1.OwnerReference
}

func NewSecretManager(c client.Client, scheme *runtime.Scheme, ownerName string, ownerUID types.UID) *SecretManager {
	return &SecretManager{
		Client: c,
		Scheme: scheme,
		Owner: metav1.OwnerReference{
			APIVersion:         "talos.yuriykovalchuk.dev/v1alpha1",
			Kind:               "TalosCluster",
			Name:               ownerName,
			UID:                ownerUID,
			Controller:         ptr.To(true),
			BlockOwnerDeletion: ptr.To(true),
		},
	}
}

func (sm *SecretManager) Create(ctx context.Context, name, namespace, key, content string, secretType corev1.SecretType) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{sm.Owner},
		},
		Type: secretType,
		Data: map[string][]byte{key: []byte(content)},
	}
	if err := sm.Client.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (sm *SecretManager) CreateOrUpdate(ctx context.Context, name, namespace, key, content string, secretType corev1.SecretType) error {
	secret := &corev1.Secret{}
	if err := sm.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return sm.Create(ctx, name, namespace, key, content, secretType)
		}
		return err
	}
	secret.Data[key] = []byte(content)
	return sm.Client.Update(ctx, secret)
}

func (sm *SecretManager) Delete(ctx context.Context, name, namespace string) error {
	secret := &corev1.Secret{}
	if err := sm.Client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return sm.Client.Delete(ctx, secret)
}

func (sm *SecretManager) DeleteMultiple(ctx context.Context, namespace string, names ...string) error {
	for _, name := range names {
		if err := sm.Delete(ctx, name, namespace); err != nil {
			return err
		}
	}
	return nil
}
