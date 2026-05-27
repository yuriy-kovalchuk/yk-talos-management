package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

type TalosNodePhase string

const (
	TalosNodePhasePending  TalosNodePhase = "Pending"
	TalosNodePhaseApplying TalosNodePhase = "Applying"
	TalosNodePhaseReady    TalosNodePhase = "Ready"
	TalosNodePhaseError    TalosNodePhase = "Error"
	// TalosNodePhaseDeleting is set as soon as the deletion finalizer starts
	// processing — drain, etcd leave, and config cleanup. The phase persists
	// until the finalizer is removed and the object is gone.
	TalosNodePhaseDeleting TalosNodePhase = "Deleting"
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
	// Name of the TalosCluster this node belongs to.
	ClusterRef string `json:"clusterRef"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=ControlPlane;Worker
	// Role of this node in the cluster.
	Role TalosNodeRole `json:"role"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Format=ipv4
	// IPv4 address of the Talos node.
	NodeIP string `json:"nodeIP"`

	// YAML patches applied on top of the base machine config.
	// Patches without an apiVersion key are deep-merged into the machine/cluster config.
	// Patches with apiVersion (e.g. RegistryMirrorConfig) are appended as separate YAML documents.
	Patches []string `json:"patches,omitempty"`

	// Secret-backed patches applied after inline patches, so sensitive values
	// (credentials, keys) do not need to be inlined in the manifest.
	// Each entry references a key within a Kubernetes Secret in the same namespace.
	PatchesFrom []corev1.SecretKeySelector `json:"patchesFrom,omitempty"`

	// +kubebuilder:default=true
	// When true, the controller periodically fetches the running config from the node
	// and re-applies if it diverges from the desired state. Set to false for nodes
	// that are frequently offline (e.g. homelab nodes powered down overnight).
	DriftDetection *bool `json:"driftDetection,omitempty"`

	// When true, skip Kubernetes node drain (cordon + pod eviction) during node removal.
	// Use for nodes that are already unreachable or when fast removal is required.
	// +optional
	// +kubebuilder:default=false
	SkipDrain bool `json:"skipDrain,omitempty"`

	// Maximum time to wait for all pods to be evicted during node drain.
	// Defaults to 5 minutes.
	// +optional
	DrainTimeout *metav1.Duration `json:"drainTimeout,omitempty"`

	// When true, the operator wipes the node's ephemeral state and reboots it into
	// maintenance mode as part of the deletion sequence (after etcd leave, before the
	// config secret is removed). Useful when the node hardware will be repurposed —
	// the next TalosNode pointing at the same IP will apply a fresh config.
	// Best-effort: a reset failure is logged and emits an event but never blocks deletion.
	// +optional
	// +kubebuilder:default=false
	ResetOnDelete bool `json:"resetOnDelete,omitempty"`
}

type TalosNodeStatus struct {
	// Current lifecycle phase of the node.
	Phase TalosNodePhase `json:"phase,omitempty"`

	// Human-readable message describing the current state.
	Message string `json:"message,omitempty"`

	// Number of failed etcd leave attempts during deletion.
	DeletionAttempts int32 `json:"deletionAttempts,omitempty"`

	CommonStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name=Role,type=string,JSONPath=.spec.role
// +kubebuilder:printcolumn:name=Phase,type=string,JSONPath=.status.phase
// +kubebuilder:resource:scope=Namespaced,path=talosnodes,shortName=talosnode

// TalosNode applies machine configuration to a single Talos Linux node and manages its lifecycle.
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
