package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type TalosPhase string

const (
	TalosPhasePending      TalosPhase = "Pending"
	TalosPhaseProvisioning TalosPhase = "Provisioning"
	TalosPhaseReady        TalosPhase = "Ready"
	TalosPhaseError        TalosPhase = "Error"
	// TalosPhaseDeleting is set when deletion is blocked waiting for TalosNode
	// objects to be removed first. The finalizer holds the object alive until
	// all nodes referencing this cluster have been deleted.
	TalosPhaseDeleting TalosPhase = "Deleting"
)

// Condition type constants as plain strings — no type casting needed at call sites.
const (
	TalosClusterConditionSecretsGenerated = "SecretsGenerated"
	TalosClusterConditionConfigsGenerated = "ConfigsGenerated"
)

type TalosClusterSpec struct {
	// +kubebuilder:validation:Required
	// Name of the Talos cluster, embedded in generated machine configs.
	ClusterName string `json:"clusterName"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// IP addresses of the control plane nodes. The first endpoint is used as the
	// Kubernetes API server address; all are embedded in the generated talosconfig.
	Endpoints []string `json:"endpoints"`

	// +kubebuilder:validation:Required
	// Talos version used when generating machine configs (e.g. v1.13.0).
	TalosVersion string `json:"talosVersion"`
}

type TalosClusterStatus struct {
	// Current lifecycle phase of the cluster.
	Phase TalosPhase `json:"phase,omitempty"`

	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=.status.phase
// +kubebuilder:resource:scope=Namespaced,path=talosclusters,shortName=taloscluster
// +kubebuilder:storageversion
// +kubebuilder:webhooks:verbs=create;update,path=/validate-talos-yuriykovalchuk-dev-v1alpha1-taloscluster,validatingWebhookGeneratorStrategy=webhook-client

// TalosCluster generates and stores the secrets bundle, machine configs, and talosconfig for a Talos Linux cluster.
type TalosCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosClusterSpec   `json:"spec,omitempty"`
	Status TalosClusterStatus `json:"status,omitempty"`
}

func (t *TalosCluster) DeepCopyObject() runtime.Object { return t }

type TalosClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosCluster `json:"items"`
}

func (t *TalosClusterList) DeepCopyObject() runtime.Object { return t }
