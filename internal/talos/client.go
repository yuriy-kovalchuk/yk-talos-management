package talos

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	machineconfig "github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Client is a Talos gRPC client. Aliased so controllers don't import the machinery package directly.
type Client = talosclient.Client

// ClusterConfigs holds the generated machine configs for a cluster.
type ClusterConfigs struct {
	ControlPlane []byte
	Worker       []byte
	Talosconfig  []byte
}

// OperationTimeout is the per-call deadline applied to Talos gRPC operations
// (ApplyConfig, Bootstrap, GetKubeconfig). Defaults to 60 s; override via
// TALOS_OPERATION_TIMEOUT env var (seconds) or set directly in tests.
var OperationTimeout = func() time.Duration {
	if v := os.Getenv("TALOS_OPERATION_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 60 * time.Second
}()

// withTimeout wraps ctx with OperationTimeout when non-zero.
func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if OperationTimeout > 0 {
		return context.WithTimeout(ctx, OperationTimeout)
	}
	return ctx, func() {}
}

// GenSecretsBundle generates a new secrets bundle for the given Talos version.
// Returns the in-memory bundle (pass directly to GenConfig) and its JSON bytes for Kubernetes storage.
// Both must come from the same call — passing the bundle to GenConfig ensures configs and secrets match.
func GenSecretsBundle(talosVersion string) (*secrets.Bundle, []byte, error) {
	contract, err := machineconfig.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, nil, fmt.Errorf("parse version: %w", err)
	}
	bundle, err := secrets.NewBundle(secrets.NewClock(), contract)
	if err != nil {
		return nil, nil, fmt.Errorf("gen secrets: %w", err)
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal secrets: %w", err)
	}
	return bundle, data, nil
}

// LoadOrGenSecretsBundle returns the bundle from existingJSON when non-empty and
// parseable, otherwise generates a fresh one.  Pass the raw JSON stored in the
// cluster secrets Kubernetes secret so that configs generated in different
// reconcile cycles always share the same CA.
func LoadOrGenSecretsBundle(existingJSON []byte, talosVersion string) (*secrets.Bundle, []byte, error) {
	if len(existingJSON) > 0 {
		bundle := &secrets.Bundle{Clock: secrets.NewClock()}
		if err := json.Unmarshal(existingJSON, bundle); err == nil {
			return bundle, existingJSON, nil
		}
	}
	return GenSecretsBundle(talosVersion)
}

// GenConfig generates controlplane, worker, and talosconfig for a cluster.
// endpoints is the list of control plane IPs; endpoints[0] is used as the Kubernetes API server
// address and all endpoints are embedded in the talosconfig so any control plane can be reached.
// bundle must be the same pointer returned by GenSecretsBundle — this guarantees all configs are
// signed by the same CA and share the same tokens.
func GenConfig(clusterName string, endpoints []string, talosVersion string, bundle *secrets.Bundle) (*ClusterConfigs, error) {
	contract, err := machineconfig.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, fmt.Errorf("parse version: %w", err)
	}
	input, err := generate.NewInput(
		clusterName,
		"https://"+endpoints[0]+":6443",
		constants.DefaultKubernetesVersion,
		generate.WithVersionContract(contract),
		generate.WithSecretsBundle(bundle),
		generate.WithEndpointList(endpoints),
	)
	if err != nil {
		return nil, fmt.Errorf("build input: %w", err)
	}

	cp, err := input.Config(machine.TypeControlPlane)
	if err != nil {
		return nil, fmt.Errorf("gen controlplane: %w", err)
	}
	w, err := input.Config(machine.TypeWorker)
	if err != nil {
		return nil, fmt.Errorf("gen worker: %w", err)
	}
	tc, err := input.Talosconfig()
	if err != nil {
		return nil, fmt.Errorf("gen talosconfig: %w", err)
	}

	cpBytes, err := cp.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encode controlplane: %w", err)
	}
	wBytes, err := w.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encode worker: %w", err)
	}
	// Use tc.Bytes() so serialization uses the same yaml/v4 library as clientconfig.FromBytes.
	tcBytes, err := tc.Bytes()
	if err != nil {
		return nil, fmt.Errorf("encode talosconfig: %w", err)
	}

	return &ClusterConfigs{ControlPlane: cpBytes, Worker: wBytes, Talosconfig: tcBytes}, nil
}

// NewClient creates a Talos gRPC client from raw talosconfig bytes read directly from a Kubernetes secret.
// No temp files needed — the bytes go straight into the client config parser.
func NewClient(ctx context.Context, talosconfigBytes []byte, endpoint string) (*Client, error) {
	cfg, err := clientconfig.FromBytes(talosconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("parse talosconfig: %w", err)
	}
	return talosclient.New(ctx, talosclient.WithConfig(cfg), talosclient.WithEndpoints(endpoint))
}

// NewClientInsecure creates a client that skips TLS certificate verification.
// Used for the very first config apply when the node is in maintenance mode
// and presents a self-signed certificate not backed by the cluster CA.
func NewClientInsecure(ctx context.Context, endpoint string) (*Client, error) {
	return talosclient.New(ctx,
		talosclient.WithDefaultGRPCDialOptions(),
		talosclient.WithTLSConfig(&tls.Config{InsecureSkipVerify: true}), //nolint:gosec
		talosclient.WithEndpoints(fmt.Sprintf("%s:%d", endpoint, constants.ApidPort)),
	)
}

// ApplyConfig applies a machine config to a node and logs the mode and any warnings from the response.
func ApplyConfig(ctx context.Context, c *Client, nodeIP string, configBytes []byte) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	l := log.FromContext(ctx)
	resp, err := c.ApplyConfiguration(talosclient.WithNode(ctx, nodeIP), &machineapi.ApplyConfigurationRequest{
		Data: configBytes,
		Mode: machineapi.ApplyConfigurationRequest_AUTO,
	})
	if err != nil {
		return err
	}
	for _, msg := range resp.GetMessages() {
		l.Info("Talos apply-config response",
			"node", nodeIP,
			"mode", msg.GetMode().String(),
			"details", msg.GetModeDetails(),
		)
		for _, w := range msg.GetWarnings() {
			l.Info("Talos config warning", "node", nodeIP, "warning", w)
		}
	}
	return nil
}

// Bootstrap triggers etcd bootstrap on the given endpoint node.
func Bootstrap(ctx context.Context, c *Client, endpoint string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	return c.Bootstrap(talosclient.WithNode(ctx, endpoint), &machineapi.BootstrapRequest{})
}

// GetKubeconfig retrieves the admin kubeconfig from the cluster.
func GetKubeconfig(ctx context.Context, c *Client, endpoint string) ([]byte, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	return c.Kubeconfig(talosclient.WithNode(ctx, endpoint))
}
