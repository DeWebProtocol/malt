// Package ipfs provides a CAS client that communicates with a local
// IPFS daemon via its API, supporting both read and write operations.
package ipfs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	cashttpapi "github.com/dewebprotocol/malt/core/cas/httpapi"
	cid "github.com/ipfs/go-cid"
)

// Client implements cas.Client using a local IPFS daemon API.
type Client struct {
	apiURL string
	client *http.Client
}

type uniqueBlock struct {
	index int
	cid   cid.Cid
	block cas.Block
}

// Option configures an IPFS client.
type Option func(*options)

type options struct {
	timeout time.Duration
}

func defaultOptions() *options {
	return &options{
		timeout: 30 * time.Second,
	}
}

// WithTimeout sets the HTTP request timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.timeout = timeout
	}
}

// NewClient creates a new IPFS daemon API client.
// apiURL is the IPFS API endpoint, e.g. "http://localhost:5001".
func NewClient(apiURL string, opts ...Option) *Client {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	return &Client{
		apiURL: apiURL,
		client: &http.Client{
			Timeout: options.timeout,
		},
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
	return c.putWithFormat(ctx, data, "")
}

// PutWithCodec stores a block under the requested CID codec.
func (c *Client) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	if codec == cid.Raw {
		return c.Put(ctx, data)
	}
	return c.putWithFormat(ctx, data, strconv.FormatUint(codec, 10))
}

func (c *Client) putWithFormat(ctx context.Context, data []byte, format string) (cid.Cid, error) {
	apiURL := fmt.Sprintf("%s/api/v0/block/put", c.apiURL)
	if format != "" {
		values := url.Values{}
		values.Set("format", format)
		values.Set("mhtype", "sha2-256")
		apiURL += "?" + values.Encode()
	}

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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, body)
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

// PutBatch stores blocks with local deduplication and batch preflight.
func (c *Client) PutBatch(ctx context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	if len(blocks) == 0 {
		return []cas.PutResult{}, nil
	}

	results := make([]cas.PutResult, len(blocks))
	unique := make([]uniqueBlock, 0, len(blocks))
	seen := make(map[string]int, len(blocks))
	for i, block := range blocks {
		blockCID, err := cas.CIDForBlock(block)
		if err != nil {
			return nil, err
		}
		results[i].CID = blockCID
		key := blockCID.String()
		if _, ok := seen[key]; ok {
			results[i].Status = cas.PutStatusDuplicate
			continue
		}
		seen[key] = i
		unique = append(unique, uniqueBlock{index: i, cid: blockCID, block: block})
	}

	uniqueCIDs := make([]cid.Cid, len(unique))
	for i, item := range unique {
		uniqueCIDs[i] = item.cid
	}
	present, err := c.HasBatch(ctx, uniqueCIDs)
	if err != nil {
		return nil, err
	}
	if len(present) != len(unique) {
		return nil, fmt.Errorf("batch has returned %d results for %d blocks", len(present), len(unique))
	}

	missing := make([]uniqueBlock, 0, len(unique))
	for i, item := range unique {
		if present[i] {
			results[item.index].Status = cas.PutStatusAlreadyPresent
			continue
		}
		missing = append(missing, item)
	}
	if len(missing) == 0 {
		return results, nil
	}

	uploaded, err := c.putBatchMissing(ctx, missing)
	if err != nil {
		return nil, err
	}
	if len(uploaded) != len(missing) {
		return nil, fmt.Errorf("batch put returned %d results for %d blocks", len(uploaded), len(missing))
	}
	for i, result := range uploaded {
		item := missing[i]
		if !result.CID.Equals(item.cid) {
			return nil, fmt.Errorf("batch put returned CID %s for block %d, want %s", result.CID, item.index, item.cid)
		}
		results[item.index] = result
	}
	return results, nil
}

func (c *Client) putBatchMissing(ctx context.Context, missing []uniqueBlock) ([]cas.PutResult, error) {
	reqBody := cashttpapi.PutBatchRequest{Blocks: make([]cashttpapi.PutBatchBlock, len(missing))}
	for i, item := range missing {
		reqBody.Blocks[i] = cashttpapi.PutBatchBlock{
			Codec: item.block.Codec,
			Data:  base64.StdEncoding.EncodeToString(item.block.Data),
		}
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal batch put request: %w", err)
	}
	apiURL := fmt.Sprintf("%s/api/v0/malt/block/put-batch", c.apiURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create batch put request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to put batch: %w", err)
	}
	defer resp.Body.Close()

	if isBatchEndpointUnsupported(resp.StatusCode) {
		io.Copy(io.Discard, resp.Body)
		return c.putBatchMissingIndividually(ctx, missing)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("MALT batch put returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp cashttpapi.PutBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode batch put response: %w", err)
	}
	results := make([]cas.PutResult, len(apiResp.Results))
	for i, item := range apiResp.Results {
		blockCID, err := cid.Decode(item.CID)
		if err != nil {
			return nil, fmt.Errorf("decode batch put CID at index %d: %w", i, err)
		}
		results[i] = cas.PutResult{CID: blockCID, Status: cas.PutStatus(item.Status)}
	}
	return results, nil
}

func (c *Client) putBatchMissingIndividually(ctx context.Context, missing []uniqueBlock) ([]cas.PutResult, error) {
	results := make([]cas.PutResult, len(missing))
	for i, item := range missing {
		codec := cas.NormalizeCodec(item.block.Codec)
		var (
			blockCID cid.Cid
			err      error
		)
		if codec == cid.Raw {
			blockCID, err = c.Put(ctx, item.block.Data)
		} else {
			blockCID, err = c.PutWithCodec(ctx, item.block.Data, codec)
		}
		if err != nil {
			return nil, err
		}
		results[i] = cas.PutResult{CID: blockCID, Status: cas.PutStatusStored}
	}
	return results, nil
}

// Has checks if a block exists in the local IPFS node.
func (c *Client) Has(ctx context.Context, cID cid.Cid) (bool, error) {
	url := fmt.Sprintf("%s/api/v0/block/stat?arg=%s", c.apiURL, cID.String())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to check block: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // drain body

	// block/stat returns 400/500 if block not found
	return resp.StatusCode == http.StatusOK, nil
}

// HasBatch checks if blocks exist, falling back to individual Has when needed.
func (c *Client) HasBatch(ctx context.Context, cids []cid.Cid) ([]bool, error) {
	if len(cids) == 0 {
		return []bool{}, nil
	}
	reqBody := cashttpapi.HasBatchRequest{CIDs: make([]string, len(cids))}
	for i, blockCID := range cids {
		reqBody.CIDs[i] = blockCID.String()
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal batch has request: %w", err)
	}
	apiURL := fmt.Sprintf("%s/api/v0/malt/block/has-batch", c.apiURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create batch has request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to check batch: %w", err)
	}
	defer resp.Body.Close()

	if isBatchEndpointUnsupported(resp.StatusCode) {
		io.Copy(io.Discard, resp.Body)
		return c.hasBatchIndividually(ctx, cids)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("MALT batch has returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp cashttpapi.HasBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode batch has response: %w", err)
	}
	if len(apiResp.Results) != len(cids) {
		return nil, fmt.Errorf("batch has returned %d results for %d CIDs", len(apiResp.Results), len(cids))
	}
	results := make([]bool, len(cids))
	for i, item := range apiResp.Results {
		if item.CID != cids[i].String() {
			return nil, fmt.Errorf("batch has returned CID %s at index %d, want %s", item.CID, i, cids[i])
		}
		results[i] = item.Present
	}
	return results, nil
}

func (c *Client) hasBatchIndividually(ctx context.Context, cids []cid.Cid) ([]bool, error) {
	results := make([]bool, len(cids))
	for i, blockCID := range cids {
		present, err := c.Has(ctx, blockCID)
		if err != nil {
			return nil, err
		}
		results[i] = present
	}
	return results, nil
}

func isBatchEndpointUnsupported(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed
}

// Ensure Client implements cas.Client.
var _ cas.Client = (*Client)(nil)
var _ cas.BatchReader = (*Client)(nil)
var _ cas.BatchWriter = (*Client)(nil)
