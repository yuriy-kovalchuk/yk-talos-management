package talos

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	appmetrics "github.com/yuriy-kovalchuk/yk-talos-management/internal/metrics"
	talosclient "github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	machineconfig "github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	resourceconfig "github.com/siderolabs/talos/pkg/machinery/resources/config"
	resourcenetwork "github.com/siderolabs/talos/pkg/machinery/resources/network"
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

const defaultOperationTimeout = 60 * time.Second

// OperationTimeout is the per-call deadline applied to Talos gRPC operations
// (ApplyConfig, Bootstrap, GetKubeconfig). Defaults to 60 s; override via
// TALOS_OPERATION_TIMEOUT env var (seconds) or set directly in tests.
var OperationTimeout = func() time.Duration {
	if v := os.Getenv("TALOS_OPERATION_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultOperationTimeout
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
func GenConfig(clusterName string, endpoints []string, talosVersion string, bundle *secrets.Bundle, kubernetesVersion string) (*ClusterConfigs, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no endpoints configured")
	}
	contract, err := machineconfig.ParseContractFromVersion(talosVersion)
	if err != nil {
		return nil, fmt.Errorf("parse version: %w", err)
	}
	k8sVersion := kubernetesVersion
	if k8sVersion == "" {
		k8sVersion = constants.DefaultKubernetesVersion
	}
	input, err := generate.NewInput(
		clusterName,
		"https://"+endpoints[0]+":6443",
		k8sVersion,
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
func ApplyConfig(ctx context.Context, c *Client, nodeIP string, configBytes []byte, cluster string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	start := time.Now()
	l := log.FromContext(ctx)
	resp, err := c.ApplyConfiguration(talosclient.WithNode(ctx, nodeIP), &machineapi.ApplyConfigurationRequest{
		Data: configBytes,
		Mode: machineapi.ApplyConfigurationRequest_AUTO,
	})
	result := "success"
	if err != nil {
		result = "error"
	}
	appmetrics.APICallDuration.WithLabelValues("apply_config", result).Observe(time.Since(start).Seconds())
	if err != nil {
		return err
	}
	for _, msg := range resp.GetMessages() {
		appmetrics.ConfigApplyModeTotal.WithLabelValues(msg.GetMode().String(), cluster).Inc()
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
	start := time.Now()
	err := c.Bootstrap(talosclient.WithNode(ctx, endpoint), &machineapi.BootstrapRequest{})
	appmetrics.APICallDuration.WithLabelValues("bootstrap", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	return err
}

// GetKubeconfig retrieves the admin kubeconfig from the cluster.
func GetKubeconfig(ctx context.Context, c *Client, endpoint string) ([]byte, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	start := time.Now()
	b, err := c.Kubeconfig(talosclient.WithNode(ctx, endpoint))
	appmetrics.APICallDuration.WithLabelValues("get_kubeconfig", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	return b, err
}

// GetMachineConfig retrieves the active running machine config from the node via the COSI resource API.
// This is equivalent to `talosctl get machineconfig v1alpha1` and works reliably on booted nodes.
func GetMachineConfig(ctx context.Context, c *Client, nodeIP string) ([]byte, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	start := time.Now()
	r, err := c.COSI.Get(
		talosclient.WithNode(ctx, nodeIP),
		resource.NewMetadata(resourceconfig.NamespaceName, resourceconfig.MachineConfigType, resourceconfig.ActiveID, resource.VersionUndefined),
	)
	appmetrics.APICallDuration.WithLabelValues("get_machine_config", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	if err != nil {
		return nil, err
	}

	mc, ok := r.(*resourceconfig.MachineConfig)
	if !ok {
		return nil, fmt.Errorf("unexpected resource type %T", r)
	}

	return mc.Provider().Bytes()
}

// GetHostname retrieves the node's hostname via the COSI network resource API.
// The hostname is the Kubernetes Node name that kubelet registered with — use
// this instead of searching by IP to find the k8s Node object reliably across
// multi-homed setups where spec.nodeIP may not match the kubelet's primary NIC.
func GetHostname(ctx context.Context, c *Client, nodeIP string) (string, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	start := time.Now()
	r, err := c.COSI.Get(
		talosclient.WithNode(ctx, nodeIP),
		resource.NewMetadata(resourcenetwork.NamespaceName, resourcenetwork.HostnameStatusType, resourcenetwork.HostnameID, resource.VersionUndefined),
	)
	appmetrics.APICallDuration.WithLabelValues("get_hostname", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	if err != nil {
		return "", err
	}

	hs, ok := r.(*resourcenetwork.HostnameStatus)
	if !ok {
		return "", fmt.Errorf("unexpected resource type %T", r)
	}

	return hs.TypedSpec().Hostname, nil
}

// GetVersion fetches the Talos version and platform mode for nodeIP.
// Returns the version tag (e.g. "v1.13.0") and the platform mode string
// (e.g. "container", "metal", "cloud").
func GetVersion(ctx context.Context, c *Client, nodeIP string) (tag, mode string, err error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	start := time.Now()
	resp, respErr := c.Version(talosclient.WithNode(ctx, nodeIP))
	appmetrics.APICallDuration.WithLabelValues("get_version", appmetrics.ResultLabel(respErr)).Observe(time.Since(start).Seconds())
	if respErr != nil {
		return "", "", respErr
	}

	for _, msg := range resp.GetMessages() {
		if v := msg.GetVersion(); v != nil {
			tag = v.GetTag()
		}
		if p := msg.GetPlatform(); p != nil {
			mode = p.GetMode()
		}
		return tag, mode, nil
	}
	return "", "", fmt.Errorf("no version message returned for node %s", nodeIP)
}

// UpgradeNode initiates an in-place Talos upgrade to the given installer image.
// The call returns as soon as the node acknowledges the request; the node will
// reboot to complete the upgrade. Mirrors `talosctl upgrade --image <image>`.
func UpgradeNode(ctx context.Context, c *Client, nodeIP, image string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	start := time.Now()
	_, err := c.Upgrade(talosclient.WithNode(ctx, nodeIP), image, false, false)
	appmetrics.APICallDuration.WithLabelValues("upgrade_node", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	return err
}

// ResetNode wipes the node's ephemeral state and reboots it into maintenance mode.
// Graceful is false so the reset proceeds even when the kubelet is in a degraded state.
// Used for the standalone annotation-triggered reset and the spec.resetOnDelete path.
func ResetNode(ctx context.Context, c *Client, nodeIP string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	start := time.Now()
	err := c.ResetGeneric(talosclient.WithNode(ctx, nodeIP), &machineapi.ResetRequest{
		Graceful: false,
		Reboot:   true,
	})
	appmetrics.APICallDuration.WithLabelValues("reset_node", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	return err
}

// EtcdLeave instructs the given node to remove itself from the etcd cluster.
// Called on the departing node during graceful removal.
func EtcdLeave(ctx context.Context, c *Client, nodeIP string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	start := time.Now()
	err := c.EtcdLeaveCluster(talosclient.WithNode(ctx, nodeIP), &machineapi.EtcdLeaveClusterRequest{})
	appmetrics.APICallDuration.WithLabelValues("etcd_leave", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	return err
}

// EtcdForceRemoveByIP lists etcd members via survivorIP, finds the member whose
// peer URL contains deadNodeIP, and removes it by ID.
// Called on a surviving node when the departing node is unreachable.
func EtcdForceRemoveByIP(ctx context.Context, c *Client, survivorIP, deadNodeIP string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()

	start := time.Now()
	resp, err := c.EtcdMemberList(talosclient.WithNode(ctx, survivorIP), &machineapi.EtcdMemberListRequest{})
	if err != nil {
		appmetrics.APICallDuration.WithLabelValues("etcd_force_remove", "error").Observe(time.Since(start).Seconds())
		return fmt.Errorf("list etcd members: %w", err)
	}

	memberID := findEtcdMemberID(resp.GetMessages(), deadNodeIP)
	if memberID == 0 {
		appmetrics.APICallDuration.WithLabelValues("etcd_force_remove", "error").Observe(time.Since(start).Seconds())
		return fmt.Errorf("etcd member with IP %s not found in membership list", deadNodeIP)
	}

	err = c.EtcdRemoveMemberByID(talosclient.WithNode(ctx, survivorIP), &machineapi.EtcdRemoveMemberByIDRequest{
		MemberId: memberID,
	})
	appmetrics.APICallDuration.WithLabelValues("etcd_force_remove", appmetrics.ResultLabel(err)).Observe(time.Since(start).Seconds())
	return err
}

// findEtcdMemberID scans member list messages and returns the ID of the member
// whose peer URL host matches deadNodeIP exactly, or 0 if not found.
//
// The URL host is parsed and compared directly so that an IP like "10.0.0.1"
// cannot falsely match a peer URL for "10.0.0.10:2380" via substring matching.
func findEtcdMemberID(messages []*machineapi.EtcdMembers, deadNodeIP string) uint64 {
	for _, msg := range messages {
		for _, m := range msg.GetMembers() {
			for _, peerURL := range m.GetPeerUrls() {
				u, err := url.Parse(peerURL)
				if err != nil {
					continue
				}
				if u.Hostname() == deadNodeIP {
					return m.GetId()
				}
			}
		}
	}
	return 0
}
