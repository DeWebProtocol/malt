// Package ipfslocal provides a CAS client that communicates with a local
// IPFS daemon via its API. Unlike the HTTP gateway client, this supports
// both read and write operations.
//
// The local IPFS daemon API is typically available at localhost:5001.
// This is the CAS backend used by the gateway when --ipfs-api is specified.
package ipfslocal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/dewebprotocol/malt/core/cas"
	cid "github.com/ipfs/go-cid"
)

// Client implements cas.Client using a local IPFS daemon API.
type Client struct {
	apiURL string
	client *http.Client
}

// NewClient creates a new IPFS local daemon client.
// apiURL is the IPFS API endpoint, e.g. "http://localhost:5001".
func NewClient(apiURL string) *Client {
	return &Client{
		apiURL: apiURL,
		client: &http.Client{},
	}
}

// Get retrieves a block from the local IPFS node.
func (c *Client) Get(ctx context.Context, cID cid.Cid) ([]byte, error) {
	url := fmt.Sprintf("%s/api/v0/block/get?arg=%s", c.apiURL, cID.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch block: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("IPFS returned status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read block: %w", err)
	}

	return data, nil
}

// Put stores a block to the local IPFS node.
func (c *Client) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	// Use /api/v0/block/put to store raw data
	// First put the block, then get its CID
	url := fmt.Sprintf("%s/api/v0/block/put", c.apiURL)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "data")
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to create form: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return cid.Cid{}, fmt.Errorf("failed to write data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return cid.Cid{}, fmt.Errorf("failed to close writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.client.Do(req)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to put block: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return cid.Cid{}, fmt.Errorf("IPFS returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Key  string `json:"Key"`
		Size int    `json:"Size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return cid.Cid{}, fmt.Errorf("failed to decode response: %w", err)
	}

	cID, err := cid.Decode(result.Key)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to parse CID: %w", err)
	}

	return cID, nil
}

// Has checks if a block exists in the local IPFS node.
func (c *Client) Has(ctx context.Context, cID cid.Cid) (bool, error) {
	// Use /api/v0/block/stat to check existence
	url := fmt.Sprintf("%s/api/v0/block/stat?arg=%s", c.apiURL, cID.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		// Network error - IPFS daemon might not be running
		return false, fmt.Errorf("failed to check block: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // drain body

	// block/stat returns 400/500 if block not found
	return resp.StatusCode == http.StatusOK, nil
}

// Ensure Client implements cas.Client.
var _ cas.Client = (*Client)(nil)
