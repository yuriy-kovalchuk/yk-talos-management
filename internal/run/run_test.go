package run

import (
	"os"
	"testing"

	"github.com/yuriy-kovalchuk/yk-talos-management/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// setOrUnsetenv sets key=value when value is non-empty, otherwise unsets the
// variable. Either way it restores the previous state via t.Cleanup, correctly
// handling the case where t.Setenv("key", "") would leave the var set (which
// matters for functions that use os.LookupEnv to distinguish set-vs-unset).
func setOrUnsetenv(t *testing.T, key, value string) {
	t.Helper()
	prev, existed := os.LookupEnv(key)
	if value != "" {
		os.Setenv(key, value) //nolint:tenv
	} else {
		os.Unsetenv(key) //nolint:tenv
	}
	t.Cleanup(func() {
		if existed {
			os.Setenv(key, prev) //nolint:tenv
		} else {
			os.Unsetenv(key) //nolint:tenv
		}
	})
}

func TestBuildScheme(t *testing.T) {
	s := buildScheme()

	if s == nil {
		t.Fatal("expected non-nil scheme")
	}

	_, err := s.New(corev1.SchemeGroupVersion.WithKind("Service"))
	if err != nil {
		t.Errorf("expected Service kind to be registered: %v", err)
	}

	_, err = s.New(v1alpha1.GroupVersion.WithKind("TalosCluster"))
	if err != nil {
		t.Errorf("expected TalosCluster kind to be registered: %v", err)
	}

	_, err = s.New(v1alpha1.GroupVersion.WithKind("TalosNode"))
	if err != nil {
		t.Errorf("expected TalosNode kind to be registered: %v", err)
	}

	_, err = s.New(v1alpha1.GroupVersion.WithKind("TalosClusterBootstrap"))
	if err != nil {
		t.Errorf("expected TalosClusterBootstrap kind to be registered: %v", err)
	}
}


func TestIsInCluster(t *testing.T) {
	tests := []struct {
		name string
		host string
		port string
		want bool
	}{
		{name: "both env vars set", host: "10.0.0.1", port: "443", want: true},
		{name: "only host set", host: "10.0.0.1", port: "", want: false},
		{name: "only port set", host: "", port: "443", want: false},
		{name: "neither set", host: "", port: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setOrUnsetenv(t, "KUBERNETES_SERVICE_HOST", tt.host)
			setOrUnsetenv(t, "KUBERNETES_PORT", tt.port)

			got := isInCluster()
			if got != tt.want {
				t.Errorf("isInCluster() = %v, want %v", got, tt.want)
			}
		})
	}
}


func TestPodNamespace(t *testing.T) {
	tests := []struct {
		name  string
		ns    string
		unset bool
		want  string
	}{
		{name: "POD_NAMESPACE set", ns: "custom-ns", want: "custom-ns"},
		{name: "POD_NAMESPACE empty - default", ns: "", want: "default"},
		{name: "POD_NAMESPACE unset - default", unset: true, want: "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unset {
				setOrUnsetenv(t, "POD_NAMESPACE", "")
			} else {
				t.Setenv("POD_NAMESPACE", tt.ns)
			}

			got := podNamespace()
			if got != tt.want {
				t.Errorf("podNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}