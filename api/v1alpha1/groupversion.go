// +groupName=talos.yuriykovalchuk.dev
// +kubebuilder:storageversion

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "talos.yuriykovalchuk.dev", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	Scheme        = runtime.NewScheme()
	AddToScheme   = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&TalosCluster{}, &TalosClusterList{})
	SchemeBuilder.Register(&TalosNode{}, &TalosNodeList{})
	SchemeBuilder.Register(&TalosClusterBootstrap{}, &TalosClusterBootstrapList{})
	if err := SchemeBuilder.AddToScheme(Scheme); err != nil {
		panic(err)
	}
}
