// Package factory wraps the Talos Image Factory HTTP API.
// It is used by the TalosNode controller to build custom installer images
// that include system extensions (iscsi-tools, linux-firmware, etc.).
package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultURL is the public Talos Image Factory endpoint.
const DefaultURL = "https://factory.talos.dev"

// Client creates Talos Image Factory schematics from a list of extension names.
// Implementations must be safe for concurrent use.
type Client interface {
	// CreateSchematic submits extensions to the Image Factory and returns the
	// resulting schematic ID. The ID is a deterministic hash of the input —
	// identical extension lists always produce the same ID.
	CreateSchematic(ctx context.Context, extensions []string) (string, error)
}

// HTTPClient is the production implementation backed by the Image Factory REST API.
type HTTPClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New returns an HTTPClient pointing at the public Image Factory with a 30-second timeout.
func New() *HTTPClient {
	return &HTTPClient{
		BaseURL:    DefaultURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewWithURL returns an HTTPClient pointing at a custom factory URL.
// Use for air-gapped or self-hosted Image Factory deployments.
func NewWithURL(baseURL string) *HTTPClient {
	return &HTTPClient{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

type schematicRequest struct {
	Customization customization `json:"customization"`
}

type customization struct {
	SystemExtensions sysExts `json:"systemExtensions"`
}

type sysExts struct {
	OfficialExtensions []string `json:"officialExtensions"`
}

type schematicResponse struct {
	ID string `json:"id"`
}

// CreateSchematic submits the extension list to the Image Factory and returns the
// schematic ID. Returns an error when the factory is unreachable, returns a non-201
// status, or returns an empty schematic ID.
func (c *HTTPClient) CreateSchematic(ctx context.Context, extensions []string) (string, error) {
	body, err := json.Marshal(schematicRequest{
		Customization: customization{
			SystemExtensions: sysExts{OfficialExtensions: extensions},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/schematics", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("post schematic: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("factory returned HTTP %d: %s", resp.StatusCode, string(b))
	}

	var sr schematicResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if sr.ID == "" {
		return "", fmt.Errorf("factory returned empty schematic ID")
	}
	return sr.ID, nil
}
