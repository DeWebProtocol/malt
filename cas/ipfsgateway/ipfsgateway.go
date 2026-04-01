// Package ipfsgateway provides an IPFS HTTP gateway client.
package ipfsgateway

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/dewebprotocol/malt/cas"
	cid "github.com/ipfs/go-cid"
)

// Client implements cas.Client using IPFS HTTP gateway.
type Client struct {
	opts   *options
	client *http.Client
}

// NewClient creates a new IPFS gateway client with the given options.
func NewClient(opts ...Option) *Client {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	return &Client{
		opts: options,
		client: &http.Client{
			Timeout: options.timeout,
		},
	}
}

// Get retrieves a block from IPFS gateway.
func (c *Client) Get(ctx context.Context, cid cid.Cid) ([]byte, error) {
	// Build gateway URL
	url := fmt.Sprintf("%s/%s", c.opts.gatewayURL, cid.String())

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
func (c *Client) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	// Gateway doesn't support PUT operations
	return cid.Cid{}, fmt.Errorf("PUT not supported via gateway-only mode")
}

// Has checks if a block exists in IPFS.
func (c *Client) Has(ctx context.Context, cid cid.Cid) (bool, error) {
	// Try HEAD request to check existence
	url := fmt.Sprintf("%s/%s", c.opts.gatewayURL, cid.String())

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