// Package cas provides Content Addressable Storage clients.
package cas

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/dewebprotocol/malt/key"
)

// Client provides access to content-addressable storage.
type Client interface {
	// Get retrieves a block by its CID.
	Get(ctx context.Context, cid key.Key) ([]byte, error)

	// Put stores a block and returns its CID.
	Put(ctx context.Context, data []byte) (key.Key, error)

	// Has checks if a block exists.
	Has(ctx context.Context, cid key.Key) (bool, error)
}

// IPFSGatewayConfig holds configuration for IPFS gateway client.
type IPFSGatewayConfig struct {
	// GatewayURL is the base URL of the IPFS gateway.
	GatewayURL string

	// Timeout is the HTTP request timeout.
	Timeout time.Duration
}

// DefaultIPFSGatewayConfig returns default configuration.
func DefaultIPFSGatewayConfig() *IPFSGatewayConfig {
	return &IPFSGatewayConfig{
		GatewayURL: "https://ipfs.io/ipfs",
		Timeout:    30 * time.Second,
	}
}

// IPFSGatewayClient implements Client using IPFS HTTP gateway.
type IPFSGatewayClient struct {
	config *IPFSGatewayConfig
	client *http.Client
}

// NewIPFSGatewayClient creates a new IPFS gateway client.
func NewIPFSGatewayClient(cfg *IPFSGatewayConfig) *IPFSGatewayClient {
	if cfg == nil {
		cfg = DefaultIPFSGatewayConfig()
	}

	return &IPFSGatewayClient{
		config: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// Get retrieves a block from IPFS gateway.
func (c *IPFSGatewayClient) Get(ctx context.Context, k key.Key) ([]byte, error) {
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
func (c *IPFSGatewayClient) Put(ctx context.Context, data []byte) (key.Key, error) {
	// Gateway doesn't support PUT operations
	// Would need local IPFS node API for this
	return nil, fmt.Errorf("PUT not supported via gateway-only mode")
}

// Has checks if a block exists in IPFS.
func (c *IPFSGatewayClient) Has(ctx context.Context, k key.Key) (bool, error) {
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

// MockCAS is a mock CAS implementation for testing.
type MockCAS struct {
	blocks map[string][]byte
}

// NewMockCAS creates a new mock CAS.
func NewMockCAS() *MockCAS {
	return &MockCAS{
		blocks: make(map[string][]byte),
	}
}

// Get retrieves a block from mock storage.
func (m *MockCAS) Get(ctx context.Context, k key.Key) ([]byte, error) {
	if k.Kind() != key.KeyKindPayloadCID {
		return nil, fmt.Errorf("expected PayloadCID, got %v", k.Kind())
	}

	data, ok := m.blocks[k.String()]
	if !ok {
		return nil, fmt.Errorf("block not found: %s", k.String())
	}

	return data, nil
}

// Put stores a block in mock storage.
func (m *MockCAS) Put(ctx context.Context, data []byte) (key.Key, error) {
	k, err := key.NewPayloadCID(data)
	if err != nil {
		return nil, err
	}

	m.blocks[k.String()] = data
	return k, nil
}

// Has checks if a block exists in mock storage.
func (m *MockCAS) Has(ctx context.Context, k key.Key) (bool, error) {
	_, ok := m.blocks[k.String()]
	return ok, nil
}

// AddBlock adds a pre-existing block to mock storage.
func (m *MockCAS) AddBlock(k key.Key, data []byte) {
	m.blocks[k.String()] = data
}