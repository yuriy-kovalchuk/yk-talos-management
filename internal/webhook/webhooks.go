package webhook

import (
	"encoding/json"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
)

// Handler is a validating admission webhook backed by a type-specific validate function.
// Adding a new webhook means writing one constructor function — no boilerplate to copy.
type Handler struct {
	validate func(raw []byte) field.ErrorList
}

var _ http.Handler = &Handler{}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := allow()
	if errs := h.validate(review.Request.Object.Raw); len(errs) > 0 {
		resp = deny(errs.ToAggregate().Error())
	}
	resp.Response.UID = review.Request.UID

	data, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data) //nolint:errcheck
}

func allow() *admissionv1.AdmissionReview {
	return &admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Response: &admissionv1.AdmissionResponse{Allowed: true},
	}
}

func deny(msg string) *admissionv1.AdmissionReview {
	return &admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Response: &admissionv1.AdmissionResponse{
			Allowed: false,
			Result:  &metav1.Status{Status: metav1.StatusFailure, Message: msg},
		},
	}
}

func unmarshalOrErr[T any](raw []byte) (T, field.ErrorList) {
	var obj T
	if err := json.Unmarshal(raw, &obj); err != nil {
		return obj, field.ErrorList{field.InternalError(field.NewPath(""), err)}
	}
	return obj, nil
}

func TalosClusterHandler() *Handler {
	return &Handler{validate: func(raw []byte) field.ErrorList {
		obj, errs := unmarshalOrErr[v1alpha1.TalosCluster](raw)
		if errs != nil {
			return errs
		}
		if obj.Spec.ClusterName == "" {
			errs = append(errs, field.Required(field.NewPath("spec", "clusterName"), ""))
		}
		if len(obj.Spec.Endpoints) == 0 {
			errs = append(errs, field.Required(field.NewPath("spec", "endpoints"), "at least one endpoint required"))
		}
		return errs
	}}
}

func TalosNodeHandler() *Handler {
	return &Handler{validate: func(raw []byte) field.ErrorList {
		obj, errs := unmarshalOrErr[v1alpha1.TalosNode](raw)
		if errs != nil {
			return errs
		}
		if obj.Spec.ClusterRef == "" {
			errs = append(errs, field.Required(field.NewPath("spec", "clusterRef"), ""))
		}
		if obj.Spec.NodeIP == "" {
			errs = append(errs, field.Required(field.NewPath("spec", "nodeIP"), ""))
		}
		if obj.Spec.Role != v1alpha1.TalosNodeRoleControlPlane && obj.Spec.Role != v1alpha1.TalosNodeRoleWorker {
			errs = append(errs, field.NotSupported(field.NewPath("spec", "role"), obj.Spec.Role,
				[]string{string(v1alpha1.TalosNodeRoleControlPlane), string(v1alpha1.TalosNodeRoleWorker)}))
		}
		return errs
	}}
}

func TalosClusterBootstrapHandler() *Handler {
	return &Handler{validate: func(raw []byte) field.ErrorList {
		obj, errs := unmarshalOrErr[v1alpha1.TalosClusterBootstrap](raw)
		if errs != nil {
			return errs
		}
		if obj.Spec.ClusterRef == "" {
			errs = append(errs, field.Required(field.NewPath("spec", "clusterRef"), ""))
		}
		return errs
	}}
}
