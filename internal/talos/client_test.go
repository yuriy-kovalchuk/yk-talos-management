package talos

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenSecretsBundle(t *testing.T) {
	t.Run("generates bundle for valid version", func(t *testing.T) {
		bundle, data, err := GenSecretsBundle("v1.13.0")
		if err != nil {
			t.Errorf("GenSecretsBundle() error = %v", err)
		}
		if bundle == nil {
			t.Error("expected non-nil bundle")
		}
		if len(data) == 0 {
			t.Error("expected non-empty data")
		}
	})

	t.Run("returns error for invalid version", func(t *testing.T) {
		_, _, err := GenSecretsBundle("invalid-version")
		if err == nil {
			t.Error("expected error for invalid version")
		}
	})
}

func TestLoadOrGenSecretsBundle(t *testing.T) {
	t.Run("returns existing when valid JSON", func(t *testing.T) {
		// Create a bundle and marshal it
		_, data, err := GenSecretsBundle("v1.13.0")
		if err != nil {
			t.Fatal(err)
		}

		gotBundle, gotData, err := LoadOrGenSecretsBundle(data, "v1.13.0")
		if err != nil {
			t.Errorf("LoadOrGenSecretsBundle() error = %v", err)
		}
		if gotBundle == nil {
			t.Error("expected non-nil bundle")
		}
		if string(gotData) != string(data) {
			t.Errorf("returned data = %s, want %s", gotData, data)
		}
	})

	t.Run("returns existing when valid but old version", func(t *testing.T) {
		_, data, err := GenSecretsBundle("v1.13.0")
		if err != nil {
			t.Fatal(err)
		}

		// Pass different version, should still return existing (doesn't re-validate version)
		gotBundle, gotData, err := LoadOrGenSecretsBundle(data, "v1.14.0")
		if err != nil {
			t.Errorf("LoadOrGenSecretsBundle() error = %v", err)
		}
		// Should return original data since JSON is valid
		if string(gotData) != string(data) {
			t.Errorf("returned data = %s, want %s", gotData, data)
		}
		_ = gotBundle
	})

	t.Run("generates new when empty", func(t *testing.T) {
		gotBundle, gotData, err := LoadOrGenSecretsBundle(nil, "v1.13.0")
		if err != nil {
			t.Errorf("LoadOrGenSecretsBundle() error = %v", err)
		}
		if gotBundle == nil {
			t.Error("expected non-nil bundle")
		}
		if len(gotData) == 0 {
			t.Error("expected non-empty data")
		}
	})

	t.Run("generates new when invalid JSON", func(t *testing.T) {
		invalidJSON := []byte("not valid json")

		gotBundle, gotData, err := LoadOrGenSecretsBundle(invalidJSON, "v1.13.0")
		if err != nil {
			t.Errorf("LoadOrGenSecretsBundle() error = %v", err)
		}
		if gotBundle == nil {
			t.Error("expected non-nil bundle")
		}
		if len(gotData) == 0 {
			t.Error("expected non-empty data")
		}
	})

	t.Run("generates new when empty string", func(t *testing.T) {
		gotBundle, gotData, err := LoadOrGenSecretsBundle([]byte(""), "v1.13.0")
		if err != nil {
			t.Errorf("LoadOrGenSecretsBundle() error = %v", err)
		}
		if gotBundle == nil {
			t.Error("expected non-nil bundle")
		}
		if len(gotData) == 0 {
			t.Error("expected non-empty data")
		}
	})
}

func TestGenConfig(t *testing.T) {
	bundle, _, err := GenSecretsBundle("v1.13.0")
	if err != nil {
		t.Fatalf("GenSecretsBundle() error = %v", err)
	}

	const (
		clusterName  = "test-cluster"
		endpoint     = "192.168.1.100"
		talosVersion = "v1.13.0"
	)

	cfg, err := GenConfig(clusterName, []string{endpoint}, talosVersion, bundle, "")
	if err != nil {
		t.Fatalf("GenConfig() error = %v", err)
	}

	if len(cfg.ControlPlane) == 0 {
		t.Error("ControlPlane config is empty")
	}
	if len(cfg.Worker) == 0 {
		t.Error("Worker config is empty")
	}
	if len(cfg.Talosconfig) == 0 {
		t.Error("Talosconfig is empty")
	}

	if bytes.Equal(cfg.ControlPlane, cfg.Worker) {
		t.Error("ControlPlane and Worker configs are identical")
	}
	if bytes.Equal(cfg.ControlPlane, cfg.Talosconfig) {
		t.Error("ControlPlane and Talosconfig are identical")
	}

	if !strings.Contains(string(cfg.ControlPlane), endpoint) {
		t.Errorf("ControlPlane config does not contain endpoint %q", endpoint)
	}

	t.Run("invalid version returns error", func(t *testing.T) {
		_, err := GenConfig(clusterName, []string{endpoint}, "invalid-version", bundle, "")
		if err == nil {
			t.Error("expected error for invalid version")
		}
	})
}