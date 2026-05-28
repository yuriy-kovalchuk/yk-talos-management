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
	TalosClusterBootstrapPhaseWaitingForAPIServer  TalosClusterBootstrapPhase = "WaitingForAPIServer"
	TalosClusterBootstrapPhaseCompleted            TalosClusterBootstrapPhase = "Completed"
	TalosClusterBootstrapPhaseError                TalosClusterBootstrapPhase = "Error"
)

// Condition type constants as plain strings — no type casting needed at call sites.
const (
	TalosClusterBootstrapConditionBootstrapped = "Bootstrapped"
	TalosClusterBootstrapConditionKubeconfig   = "KubeconfigAvailable"
	TalosClusterBootstrapConditionAPIServer    = "APIServerReady"
)

type TalosClusterBootstrapSpec struct {
	// +kubebuilder:validation:Required
	// Name of the TalosCluster this bootstrap belongs to.
	ClusterRef string `json:"clusterRef"`
}

type TalosClusterBootstrapStatus struct {
	// Current lifecycle phase of the bootstrap process.
	Phase TalosClusterBootstrapPhase `json:"phase,omitempty"`

	// Human-readable message describing the current state.
	Message string `json:"message,omitempty"`

	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Cluster,type=string,JSONPath=.spec.clusterRef
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=.status.phase
// +kubebuilder:resource:scope=Namespaced,path=talosclusterbootstraps,shortName=talosclusterbootstrap

// TalosClusterBootstrap bootstraps etcd on the first control plane node and stores the kubeconfig.
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
