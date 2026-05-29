package controller

import (
	"context"
	"fmt"

	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// TalosDialer creates connections to Talos nodes.
type TalosDialer interface {
	Dial(ctx context.Context, talosconfigBytes []byte, endpoint string) (TalosConnection, error)
	DialInsecure(ctx context.Context, endpoint string) (TalosConnection, error)
}

// TalosConnection is an active connection to a Talos node.
type TalosConnection interface {
	ApplyConfig(ctx context.Context, nodeIP string, cfg []byte, cluster string) error
	Bootstrap(ctx context.Context, endpoint string) error
	GetKubeconfig(ctx context.Context, endpoint string) ([]byte, error)
	GetMachineConfig(ctx context.Context, nodeIP string) ([]byte, error)
	GetHostname(ctx context.Context, nodeIP string) (string, error)
	// GetVersion returns the Talos version tag (e.g. "v1.13.0") and platform mode
	// (e.g. "container", "metal") running on nodeIP.
	GetVersion(ctx context.Context, nodeIP string) (tag, mode string, err error)
	EtcdLeave(ctx context.Context, nodeIP string) error
	EtcdForceRemove(ctx context.Context, survivorIP, deadNodeIP string) error
	// Reset wipes the node's STATE and EPHEMERAL partitions and reboots into
	// maintenance mode. graceful=true stops services cleanly first (use for
	// healthy nodes); graceful=false skips service shutdown (use when degraded).
	Reset(ctx context.Context, nodeIP string, graceful bool) error
	// Upgrade initiates an in-place Talos upgrade to the given installer image.
	Upgrade(ctx context.Context, nodeIP, image string) error
	Close() error
}

// RealDialer is the production TalosDialer that connects to actual Talos nodes.
type RealDialer struct{}

func (RealDialer) Dial(ctx context.Context, talosconfigBytes []byte, endpoint string) (TalosConnection, error) {
	c, err := talos.NewClient(ctx, talosconfigBytes, endpoint)
	if err != nil {
		return nil, err
	}
	return &realConnection{c: c}, nil
}

func (RealDialer) DialInsecure(ctx context.Context, endpoint string) (TalosConnection, error) {
	c, err := talos.NewClientInsecure(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &realConnection{c: c}, nil
}

type realConnection struct {
	c *talos.Client
}

func (r *realConnection) ApplyConfig(ctx context.Context, nodeIP string, cfg []byte, cluster string) error {
	return talos.ApplyConfig(ctx, r.c, nodeIP, cfg, cluster)
}

func (r *realConnection) Bootstrap(ctx context.Context, endpoint string) error {
	return talos.Bootstrap(ctx, r.c, endpoint)
}

func (r *realConnection) GetKubeconfig(ctx context.Context, endpoint string) ([]byte, error) {
	return talos.GetKubeconfig(ctx, r.c, endpoint)
}

func (r *realConnection) GetMachineConfig(ctx context.Context, nodeIP string) ([]byte, error) {
	return talos.GetMachineConfig(ctx, r.c, nodeIP)
}

func (r *realConnection) GetHostname(ctx context.Context, nodeIP string) (string, error) {
	return talos.GetHostname(ctx, r.c, nodeIP)
}

func (r *realConnection) EtcdLeave(ctx context.Context, nodeIP string) error {
	return talos.EtcdLeave(ctx, r.c, nodeIP)
}

func (r *realConnection) EtcdForceRemove(ctx context.Context, survivorIP, deadNodeIP string) error {
	return talos.EtcdForceRemoveByIP(ctx, r.c, survivorIP, deadNodeIP)
}

func (r *realConnection) GetVersion(ctx context.Context, nodeIP string) (string, string, error) {
	return talos.GetVersion(ctx, r.c, nodeIP)
}

func (r *realConnection) Reset(ctx context.Context, nodeIP string, graceful bool) error {
	return talos.ResetNode(ctx, r.c, nodeIP, graceful)
}

func (r *realConnection) Upgrade(ctx context.Context, nodeIP, image string) error {
	return talos.UpgradeNode(ctx, r.c, nodeIP, image)
}

func (r *realConnection) Close() error {
	return r.c.Close()
}

// dialAny tries each endpoint in order, returning the first successful connection.
func dialAny(ctx context.Context, dialer TalosDialer, talosconfig []byte, endpoints []string) (TalosConnection, string, error) {
	var lastErr error
	for _, ep := range endpoints {
		conn, err := dialer.Dial(ctx, talosconfig, ep)
		if err == nil {
			return conn, ep, nil
		}
		lastErr = fmt.Errorf("dial %s: %w", ep, err)
	}
	return nil, "", lastErr
}
