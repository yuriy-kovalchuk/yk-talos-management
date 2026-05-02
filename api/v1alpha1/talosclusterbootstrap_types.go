package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type TalosClusterBootstrapPhase string

const (
	TalosClusterBootstrapPhasePending              TalosClusterBootstrapPhase = "Pending"
	TalosClusterBootstrapPhaseWaitingForNodes      TalosClusterBootstrapPhase = "WaitingForNodes"
	TalosClusterBootstrapPhaseBootstrapping        TalosClusterBootstrapPhase = "Bootstrapping"
	TalosClusterBootstrapPhaseWaitingForKubeconfig TalosClusterBootstrapPhase = "WaitingForKubeconfig"
	TalosClusterBootstrapPhaseCompleted            TalosClusterBootstrapPhase = "Completed"
	TalosClusterBootstrapPhaseError                TalosClusterBootstrapPhase = "Error"
)

// Condition type constants as plain strings — no type casting needed at call sites.
const (
	TalosClusterBootstrapConditionBootstrapped = "Bootstrapped"
	TalosClusterBootstrapConditionKubeconfig   = "KubeconfigAvailable"
)

type TalosClusterBootstrapSpec struct {
	// +kubebuilder:validation:Required
	ClusterRef string `json:"clusterRef"`
}

type TalosClusterBootstrapStatus struct {
	Phase        TalosClusterBootstrapPhase `json:"phase,omitempty"`
	Message      string                     `json:"message,omitempty"`
	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Cluster,type=string,JSONPath=.spec.clusterRef
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=.status.phase
// +kubebuilder:resource:scope=Namespaced,path=talosclusterbootstraps,shortName=talosclusterbootstrap
// +kubebuilder:webhooks:verbs=create;update,path=/validate-talos-yuriykovalchuk-dev-v1alpha1-talosclusterbootstrap,validatingWebhookGeneratorStrategy=webhook-client

type TalosClusterBootstrap struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosClusterBootstrapSpec   `json:"spec,omitempty"`
	Status TalosClusterBootstrapStatus `json:"status,omitempty"`
}

func (t *TalosClusterBootstrap) DeepCopyObject() runtime.Object { return t }

type TalosClusterBootstrapList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosClusterBootstrap `json:"items"`
}

func (t *TalosClusterBootstrapList) DeepCopyObject() runtime.Object { return t }
