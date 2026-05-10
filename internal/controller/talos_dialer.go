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
	EtcdLeave(ctx context.Context, nodeIP string) error
	EtcdForceRemove(ctx context.Context, survivorIP, deadNodeIP string) error
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

func (r *realConnection) EtcdLeave(ctx context.Context, nodeIP string) error {
	return talos.EtcdLeave(ctx, r.c, nodeIP)
}

func (r *realConnection) EtcdForceRemove(ctx context.Context, survivorIP, deadNodeIP string) error {
	return talos.EtcdForceRemoveByIP(ctx, r.c, survivorIP, deadNodeIP)
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
