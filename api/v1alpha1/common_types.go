package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CommonStatus holds status fields shared by all three CRD types.
// Embed with json:",inline" so the fields are inlined in the parent JSON — no breaking API change.
type CommonStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +kubebuilder:validation:Minimum=0
	RetryCount     int32              `json:"retryCount,omitempty"`
	LastUpdateTime *metav1.Time       `json:"lastUpdateTime,omitempty"`
	Conditions     []metav1.Condition `json:"conditions,omitempty"`
}
