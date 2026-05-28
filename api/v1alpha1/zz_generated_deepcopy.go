// Code generated manually — run `controller-gen object:headerFile=... paths=./api/...`
// to replace with fully generated output.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ── CommonStatus ─────────────────────────────────────────────────────────────

func (in *CommonStatus) DeepCopyInto(out *CommonStatus) {
	*out = *in
	if in.LastUpdateTime != nil {
		t := *in.LastUpdateTime
		out.LastUpdateTime = &t
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		copy(out.Conditions, in.Conditions)
	}
}

// ── TalosCluster ─────────────────────────────────────────────────────────────

func (in *TalosClusterSpec) DeepCopyInto(out *TalosClusterSpec) {
	*out = *in
	if in.Endpoints != nil {
		out.Endpoints = make([]string, len(in.Endpoints))
		copy(out.Endpoints, in.Endpoints)
	}
}

func (in *TalosClusterStatus) DeepCopyInto(out *TalosClusterStatus) {
	*out = *in
	in.CommonStatus.DeepCopyInto(&out.CommonStatus)
}

func (in *TalosCluster) DeepCopyInto(out *TalosCluster) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *TalosCluster) DeepCopy() *TalosCluster {
	if in == nil {
		return nil
	}
	out := new(TalosCluster)
	in.DeepCopyInto(out)
	return out
}

func (in *TalosCluster) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *TalosClusterList) DeepCopyInto(out *TalosClusterList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]TalosCluster, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *TalosClusterList) DeepCopy() *TalosClusterList {
	if in == nil {
		return nil
	}
	out := new(TalosClusterList)
	in.DeepCopyInto(out)
	return out
}

func (in *TalosClusterList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// ── TalosNode ─────────────────────────────────────────────────────────────────

func (in *TalosNodeSpec) DeepCopyInto(out *TalosNodeSpec) {
	*out = *in
	if in.Patches != nil {
		out.Patches = make([]string, len(in.Patches))
		copy(out.Patches, in.Patches)
	}
	if in.PatchesFrom != nil {
		out.PatchesFrom = make([]corev1.SecretKeySelector, len(in.PatchesFrom))
		for i := range in.PatchesFrom {
			in.PatchesFrom[i].DeepCopyInto(&out.PatchesFrom[i])
		}
	}
	if in.DriftDetection != nil {
		v := *in.DriftDetection
		out.DriftDetection = &v
	}
	if in.DrainTimeout != nil {
		v := *in.DrainTimeout
		out.DrainTimeout = &v
	}
	if in.SystemExtensions != nil {
		out.SystemExtensions = make([]string, len(in.SystemExtensions))
		copy(out.SystemExtensions, in.SystemExtensions)
	}
}

func (in *TalosNodeStatus) DeepCopyInto(out *TalosNodeStatus) {
	*out = *in
	if in.InstalledExtensions != nil {
		out.InstalledExtensions = make([]string, len(in.InstalledExtensions))
		copy(out.InstalledExtensions, in.InstalledExtensions)
	}
	in.CommonStatus.DeepCopyInto(&out.CommonStatus)
}

func (in *TalosNode) DeepCopyInto(out *TalosNode) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *TalosNode) DeepCopy() *TalosNode {
	if in == nil {
		return nil
	}
	out := new(TalosNode)
	in.DeepCopyInto(out)
	return out
}

func (in *TalosNode) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *TalosNodeList) DeepCopyInto(out *TalosNodeList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]TalosNode, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *TalosNodeList) DeepCopy() *TalosNodeList {
	if in == nil {
		return nil
	}
	out := new(TalosNodeList)
	in.DeepCopyInto(out)
	return out
}

func (in *TalosNodeList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// ── TalosClusterBootstrap ────────────────────────────────────────────────────

func (in *TalosClusterBootstrapStatus) DeepCopyInto(out *TalosClusterBootstrapStatus) {
	*out = *in
	in.CommonStatus.DeepCopyInto(&out.CommonStatus)
}

func (in *TalosClusterBootstrap) DeepCopyInto(out *TalosClusterBootstrap) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = in.Spec
	in.Status.DeepCopyInto(&out.Status)
}

func (in *TalosClusterBootstrap) DeepCopy() *TalosClusterBootstrap {
	if in == nil {
		return nil
	}
	out := new(TalosClusterBootstrap)
	in.DeepCopyInto(out)
	return out
}

func (in *TalosClusterBootstrap) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *TalosClusterBootstrapList) DeepCopyInto(out *TalosClusterBootstrapList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]TalosClusterBootstrap, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *TalosClusterBootstrapList) DeepCopy() *TalosClusterBootstrapList {
	if in == nil {
		return nil
	}
	out := new(TalosClusterBootstrapList)
	in.DeepCopyInto(out)
	return out
}

func (in *TalosClusterBootstrapList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
