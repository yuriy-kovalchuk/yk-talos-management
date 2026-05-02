package controller

import (
	"context"

	"github.com/yuriy-kovalchuk/yk-talos-management/internal/talos"
)

// TalosDialer creates connections to Talos nodes.
type TalosDialer interface {
	Dial(ctx context.Context, talosconfigBytes []byte, endpoint string) (TalosConnection, error)
	DialInsecure(ctx context.Context, endpoint string) (TalosConnection, error)
}

// TalosConnection is an active connection to a Talos node.
type TalosConnection interface {
	ApplyConfig(ctx context.Context, nodeIP string, cfg []byte) error
	Bootstrap(ctx context.Context, endpoint string) error
	GetKubeconfig(ctx context.Context, endpoint string) ([]byte, error)
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

func (r *realConnection) ApplyConfig(ctx context.Context, nodeIP string, cfg []byte) error {
	return talos.ApplyConfig(ctx, r.c, nodeIP, cfg)
}

func (r *realConnection) Bootstrap(ctx context.Context, endpoint string) error {
	return talos.Bootstrap(ctx, r.c, endpoint)
}

func (r *realConnection) GetKubeconfig(ctx context.Context, endpoint string) ([]byte, error) {
	return talos.GetKubeconfig(ctx, r.c, endpoint)
}

func (r *realConnection) Close() error {
	return r.c.Close()
}
