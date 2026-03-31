// Package ipfsgateway provides an IPFS HTTP gateway client.
package ipfsgateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dewebprotocol/malt/internal/cas"
	"github.com/dewebprotocol/malt/key"
)

// Config holds configuration for IPFS gateway client.
type Config struct {
	// GatewayURL is the base URL of the IPFS gateway.
	GatewayURL string

	// Timeout is the HTTP request timeout.
	Timeout time.Duration
}

// DefaultConfig returns default configuration.
func DefaultConfig() *Config {
	return &Config{
		GatewayURL: "https://ipfs.io/ipfs",
		Timeout:    30 * time.Second,
	}
}

// Client implements cas.Client using IPFS HTTP gateway.
type Client struct {
	config *Config
	client *http.Client
}

// NewClient creates a new IPFS gateway client.
func NewClient(cfg *Config) *Client {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	return &Client{
		config: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Get retrieves a block from IPFS gateway.
func (c *Client) Get(ctx context.Context, k key.Key) ([]byte, error) {
	if k.Kind() != key.KeyKindPayloadCID {
		return nil, fmt.Errorf("expected PayloadCID, got %v", k.Kind())
	}

	// Build gateway URL
	url := fmt.Sprintf("%s/%s", c.config.GatewayURL, k.String())

	// Make HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch block: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned status %d", resp.StatusCode)
	}

	// Read block content
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read block: %w", err)
	}

	return data, nil
}

// Put stores a block via IPFS (requires local IPFS node with API).
// For gateway-only mode, this returns an error.
func (c *Client) Put(ctx context.Context, data []byte) (key.Key, error) {
	// Gateway doesn't support PUT operations
	return nil, fmt.Errorf("PUT not supported via gateway-only mode")
}

// Has checks if a block exists in IPFS.
func (c *Client) Has(ctx context.Context, k key.Key) (bool, error) {
	if k.Kind() != key.KeyKindPayloadCID {
		return false, fmt.Errorf("expected PayloadCID, got %v", k.Kind())
	}

	// Try HEAD request to check existence
	url := fmt.Sprintf("%s/%s", c.config.GatewayURL, k.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to check block: %w", err)
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

// Ensure Client implements cas.Client.
var _ cas.Client = (*Client)(nil)