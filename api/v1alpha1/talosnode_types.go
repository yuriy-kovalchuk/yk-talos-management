package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type TalosNodePhase string

const (
	TalosNodePhasePending  TalosNodePhase = "Pending"
	TalosNodePhaseApplying TalosNodePhase = "Applying"
	TalosNodePhaseReady    TalosNodePhase = "Ready"
	TalosNodePhaseError    TalosNodePhase = "Error"
)

type TalosNodeRole string

const (
	TalosNodeRoleControlPlane TalosNodeRole = "ControlPlane"
	TalosNodeRoleWorker       TalosNodeRole = "Worker"
)

// Condition type constant as a plain string — no type casting needed at call sites.
const TalosNodeConditionConfigApplied = "ConfigApplied"

type TalosNodeSpec struct {
	// +kubebuilder:validation:Required
	ClusterRef string `json:"clusterRef"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ControlPlane;Worker
	Role TalosNodeRole `json:"role"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=ipv4
	NodeIP string `json:"nodeIP"`

	Patches []string `json:"patches,omitempty"`

	// +kubebuilder:default=true
	DriftDetection *bool `json:"driftDetection,omitempty"`
}

type TalosNodeStatus struct {
	Phase            TalosNodePhase `json:"phase,omitempty"`
	Message          string         `json:"message,omitempty"`
	DeletionAttempts int32          `json:"deletionAttempts,omitempty"`
	CommonStatus     `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Role,type=string,JSONPath=.spec.role
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=.status.phase
// +kubebuilder:resource:scope=Namespaced,path=talosnodes,shortName=talosnode

type TalosNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TalosNodeSpec   `json:"spec,omitempty"`
	Status TalosNodeStatus `json:"status,omitempty"`
}

func (t *TalosNode) DeepCopyObject() runtime.Object { return t }

type TalosNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TalosNode `json:"items"`
}

func (t *TalosNodeList) DeepCopyObject() runtime.Object { return t }
