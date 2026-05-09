package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CommonStatus holds status fields shared by all three CRD types.
// Embed with json:",inline" so the fields are inlined in the parent JSON — no breaking API change.
type CommonStatus struct {
	// Generation last observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +kubebuilder:validation:Minimum=0
	// Number of consecutive failed reconcile attempts.
	RetryCount int32 `json:"retryCount,omitempty"`

	// Timestamp of the last successful reconcile.
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`

	// Current service state of the resource.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
