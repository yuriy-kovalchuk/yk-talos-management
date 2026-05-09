package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
)

func TestTalosClusterHandler(t *testing.T) {
	tests := []struct {
		name       string
		clusterName string
		endpoints  []string
		wantAllow bool
	}{
		{
			name:        "valid cluster",
			clusterName: "my-cluster",
			endpoints:   []string{"10.0.0.1"},
			wantAllow:   true,
		},
		{
			name:        "empty cluster name",
			clusterName: "",
			endpoints:   []string{"10.0.0.1"},
			wantAllow:  false,
		},
		{
			name:        "empty endpoints",
			clusterName: "my-cluster",
			endpoints:   []string{},
			wantAllow:  false,
		},
		{
			name:        "multiple endpoints",
			clusterName: "my-cluster",
			endpoints:   []string{"10.0.0.1", "10.0.0.2"},
			wantAllow:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := v1alpha1.TalosCluster{
				Spec: v1alpha1.TalosClusterSpec{
					ClusterName: tt.clusterName,
					Endpoints: tt.endpoints,
				},
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}

			handler := TalosClusterHandler()
			resp := handler.validate(raw)

			gotAllow := len(resp) == 0
			if gotAllow != tt.wantAllow {
				t.Errorf("validate() allow = %v, want %v, errors: %v", gotAllow, tt.wantAllow, resp)
			}
		})
	}
}

func TestTalosNodeHandler(t *testing.T) {
	tests := []struct {
		name      string
		clusterRef string
		nodeIP   string
		role    v1alpha1.TalosNodeRole
		wantAllow bool
	}{
		{
			name:      "valid node",
			clusterRef: "my-cluster",
			nodeIP:   "10.0.0.1",
			role:    v1alpha1.TalosNodeRoleControlPlane,
			wantAllow: true,
		},
		{
			name:      "empty cluster ref",
			clusterRef: "",
			nodeIP:   "10.0.0.1",
			role:    v1alpha1.TalosNodeRoleControlPlane,
			wantAllow: false,
		},
		{
			name:      "empty node IP",
			clusterRef: "my-cluster",
			nodeIP:   "",
			role:    v1alpha1.TalosNodeRoleControlPlane,
			wantAllow: false,
		},
		{
			name:      "invalid role",
			clusterRef: "my-cluster",
			nodeIP:   "10.0.0.1",
			role:    "Invalid",
			wantAllow: false,
		},
		{
			name:      "worker role valid",
			clusterRef: "my-cluster",
			nodeIP:   "10.0.0.1",
			role:    v1alpha1.TalosNodeRoleWorker,
			wantAllow: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := v1alpha1.TalosNode{
				Spec: v1alpha1.TalosNodeSpec{
					ClusterRef: tt.clusterRef,
					NodeIP:   tt.nodeIP,
					Role:    tt.role,
				},
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}

			handler := TalosNodeHandler()
			resp := handler.validate(raw)

			gotAllow := len(resp) == 0
			if gotAllow != tt.wantAllow {
				t.Errorf("validate() allow = %v, want %v, errors: %v", gotAllow, tt.wantAllow, resp)
			}
		})
	}
}

func TestTalosNodeHandler_PatchesMutuallyExclusive(t *testing.T) {
	obj := v1alpha1.TalosNode{
		Spec: v1alpha1.TalosNodeSpec{
			ClusterRef: "my-cluster",
			NodeIP:     "10.0.0.1",
			Role:       v1alpha1.TalosNodeRoleControlPlane,
			Patches:    []string{"machine:\n  hostname: x\n"},
			PatchesFrom: []corev1.SecretKeySelector{
				{LocalObjectReference: corev1.LocalObjectReference{Name: "s"}, Key: "k"},
			},
		},
	}
	raw, _ := json.Marshal(obj)
	if errs := TalosNodeHandler().validate(raw); len(errs) == 0 {
		t.Error("expected validation error when both patches and patchesFrom are set")
	}
}

func TestTalosClusterBootstrapHandler(t *testing.T) {
	tests := []struct {
		name       string
		clusterRef string
		wantAllow bool
	}{
		{
			name:       "valid bootstrap",
			clusterRef: "my-cluster",
			wantAllow:  true,
		},
		{
			name:       "empty cluster ref",
			clusterRef: "",
			wantAllow:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := v1alpha1.TalosClusterBootstrap{
				Spec: v1alpha1.TalosClusterBootstrapSpec{
					ClusterRef: tt.clusterRef,
				},
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}

			handler := TalosClusterBootstrapHandler()
			resp := handler.validate(raw)

			gotAllow := len(resp) == 0
			if gotAllow != tt.wantAllow {
				t.Errorf("validate() allow = %v, want %v, errors: %v", gotAllow, tt.wantAllow, resp)
			}
		})
	}
}

func makeReviewBody(t *testing.T, raw []byte, uid string) *bytes.Reader {
	t.Helper()
	review := admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			UID:    types.UID(uid),
			Object: runtime.RawExtension{Raw: raw},
		},
	}
	body, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body)
}

func decodeReviewResponse(t *testing.T, body []byte) *admissionv1.AdmissionReview {
	t.Helper()
	var resp admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to decode AdmissionReview response: %v", err)
	}
	return &resp
}

func TestHandlerServeHTTP(t *testing.T) {
	t.Run("returns 400 on bad JSON", func(t *testing.T) {
		handler := TalosClusterHandler()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("returns allowed on valid request", func(t *testing.T) {
		obj := v1alpha1.TalosCluster{
			Spec: v1alpha1.TalosClusterSpec{
				ClusterName: "my-cluster",
				Endpoints:   []string{"10.0.0.1"},
			},
		}
		raw, _ := json.Marshal(obj)

		handler := TalosClusterHandler()
		req := httptest.NewRequest(http.MethodPost, "/", makeReviewBody(t, raw, "test-uid"))
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want %q", ct, "application/json")
		}
		resp := decodeReviewResponse(t, w.Body.Bytes())
		if !resp.Response.Allowed {
			t.Error("expected allowed = true")
		}
		if string(resp.Response.UID) != "test-uid" {
			t.Errorf("expected UID = test-uid, got %s", resp.Response.UID)
		}
	})

	t.Run("returns denied on invalid request", func(t *testing.T) {
		obj := v1alpha1.TalosCluster{
			Spec: v1alpha1.TalosClusterSpec{
				ClusterName: "",
				Endpoints:   []string{},
			},
		}
		raw, _ := json.Marshal(obj)

		handler := TalosClusterHandler()
		req := httptest.NewRequest(http.MethodPost, "/", makeReviewBody(t, raw, "test-uid"))
		w := httptest.NewRecorder()

		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
		resp := decodeReviewResponse(t, w.Body.Bytes())
		if resp.Response.Allowed {
			t.Error("expected allowed = false")
		}
	})
}