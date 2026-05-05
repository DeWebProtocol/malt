// Package client provides a thin HTTP client for the local MALT daemon.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/httpapi"
)

// Error is a structured daemon API error.
type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("daemon API error (%d): %s", e.StatusCode, e.Message)
}

// Client is a thin HTTP client over the local daemon API.
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates a client from config.
func New(cfg *config.Config) *Client {
	return NewWithBaseURL(cfg.APIBaseURL())
}

// NewWithBaseURL creates a client from a base URL.
func NewWithBaseURL(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// Health checks daemon health.
func (c *Client) Health(ctx context.Context) (*httpapi.HealthResponse, error) {
	var resp httpapi.HealthResponse
	if err := c.do(ctx, http.MethodGet, "/health", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// MetricsSnapshot returns node-local daemon evaluation counters.
func (c *Client) MetricsSnapshot(ctx context.Context) (*httpapi.MetricsResponse, error) {
	var resp httpapi.MetricsResponse
	if err := c.do(ctx, http.MethodGet, "/metrics", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResetMetrics clears node-local daemon evaluation counters and returns the
// post-reset snapshot.
func (c *Client) ResetMetrics(ctx context.Context) (*httpapi.MetricsResponse, error) {
	var resp httpapi.MetricsResponse
	if err := c.do(ctx, http.MethodPost, "/metrics:reset", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResolveRoot resolves a path from an explicit root.
func (c *Client) ResolveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	return c.Resolve(ctx, root, p)
}

// ProveRoot returns the transcript for an explicit root path.
func (c *Client) ProveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	var resp httpapi.ResolveResponse
	if err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(root)+"/"+p, map[string]string{"format": "resolve"}, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ProofListRoot returns a ProofList read result from an explicit root.
func (c *Client) ProofListRoot(ctx context.Context, root string, p string) (*httpapi.ProofListResponse, error) {
	return c.ProofList(ctx, root, p)
}

// Resolve resolves a path relative to a root CID. Uses HEAD /{root}/{path}
// and returns the resolved key via X-Malt-Key response header.
func (c *Client) Resolve(ctx context.Context, root, rawPath string) (*httpapi.ResolveResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "/"+url.PathEscape(root)+"/"+rawPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return nil, &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		return nil, &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}

	return &httpapi.ResolveResponse{Target: resp.Header.Get("X-Malt-Key")}, nil
}

// ProofList resolves a path and returns a verifier-facing ProofList.
func (c *Client) ProofList(ctx context.Context, root, rawPath string) (*httpapi.ProofListResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "/"+url.PathEscape(root)+"/"+rawPath)
	q := u.Query()
	q.Set("format", "proof")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return nil, &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		return nil, &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}

	var out httpapi.ContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &httpapi.ProofListResponse{Target: out.Key, ProofList: out.ProofList}, nil
}

// Stat returns the locked stat contract for a path under a root CID.
func (c *Client) Stat(ctx context.Context, root, rawPath string) (*httpapi.PathStatResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "/"+url.PathEscape(root)+"/"+rawPath)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return nil, &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		return nil, &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}

	stat := &httpapi.PathStatResponse{
		Kind:        resp.Header.Get("X-Malt-Kind"),
		StorageKind: resp.Header.Get("X-Malt-Storage-Kind"),
		Key:         resp.Header.Get("X-Malt-Key"),
		Payload:     resp.Header.Get("X-Malt-Payload"),
	}
	if size := resp.Header.Get("Content-Length"); size != "" {
		if parsed, err := strconv.ParseInt(size, 10, 64); err == nil {
			stat.Size = &parsed
		}
	}
	return stat, nil
}

// Content reads raw content bytes for a path under a root CID.
func (c *Client) Content(ctx context.Context, root, rawPath, rangeHeader string) (io.ReadCloser, int, http.Header, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, 0, nil, err
	}
	u.Path = path.Join(u.Path, fmt.Sprintf("/%s/%s", url.PathEscape(root), rawPath))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, nil, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return nil, resp.StatusCode, resp.Header, &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		return nil, resp.StatusCode, resp.Header, &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}

	return resp.Body, resp.StatusCode, resp.Header, nil
}

// GetContent reads all content bytes for a path under a root CID.
func (c *Client) GetContent(ctx context.Context, root, rawPath, rangeHeader string) ([]byte, int, http.Header, error) {
	body, status, headers, err := c.Content(ctx, root, rawPath, rangeHeader)
	if err != nil {
		return nil, status, headers, err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, status, headers, err
	}
	return data, status, headers, nil
}

// ContentProof reads content with a content-range and ProofList for a root CID path.
func (c *Client) ContentProof(ctx context.Context, root, rawPath, rangeHeader string) (*httpapi.ContentProofResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, fmt.Sprintf("/%s/%s", url.PathEscape(root), rawPath))
	q := u.Query()
	q.Set("format", "proof")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return nil, &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		return nil, &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}

	var out httpapi.ContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateRoot updates a single path under an explicit root (PUT /{root}/{path}).
func (c *Client) UpdateRoot(ctx context.Context, root string, path string, target string) (*httpapi.WriteUpdateResponse, error) {
	var resp httpapi.WriteUpdateResponse
	if err := c.do(ctx, http.MethodPut, "/"+url.PathEscape(root)+"/"+path, nil, &httpapi.UpdateRequest{Target: target}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BatchUpdateRoot performs a batch update under an explicit root.
func (c *Client) BatchUpdateRoot(ctx context.Context, root string, updates map[string]string) (*httpapi.WriteBatchResponse, error) {
	var resp httpapi.WriteBatchResponse
	if err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(root)+"/_batch-update", nil, &httpapi.BatchUpdateRequest{Updates: updates}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ApplyRootSemanticMutation materializes a semantic mutation under an explicit root.
func (c *Client) ApplyRootSemanticMutation(ctx context.Context, root string, req *httpapi.SemanticMutationRequest) (*httpapi.SemanticMutationResponse, error) {
	var resp httpapi.SemanticMutationResponse
	if err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(root)+"/_mutate", nil, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AddUnixFSFile uploads a file into a root's UnixFS tree.
func (c *Client) AddUnixFSFile(ctx context.Context, root, rawPath string, data []byte) (*httpapi.UnixFSWriteResponse, error) {
	var resp httpapi.UnixFSWriteResponse
	if err := c.doRaw(ctx, http.MethodPost, "/"+url.PathEscape(root)+"/"+rawPath, nil, "application/octet-stream", bytes.NewReader(data), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AddUnixFSDirectory creates a directory node in a root's UnixFS tree.
func (c *Client) AddUnixFSDirectory(ctx context.Context, root, rawPath string) (*httpapi.UnixFSWriteResponse, error) {
	var resp httpapi.UnixFSWriteResponse
	if err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(root)+"/"+rawPath, map[string]string{"type": "dir"}, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateRootStructure creates a root-scoped structure.
func (c *Client) CreateRootStructure(ctx context.Context, arcs map[string]string) (*httpapi.CreateStructureResponse, error) {
	var resp httpapi.CreateStructureResponse
	if err := c.do(ctx, http.MethodPost, "/_", nil, &httpapi.CreateStructureRequest{Arcs: arcs}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreatePayloadRoot creates a minimal valid map root with an empty @payload and
// optional extra bindings. It is a convenience helper for tests and bootstrapping.
func (c *Client) CreatePayloadRoot(ctx context.Context, extras map[string]string) (*httpapi.CreateStructureResponse, error) {
	arcs := make(map[string]string, len(extras)+1)
	for k, v := range extras {
		arcs[k] = v
	}
	arcs["@payload"] = "bafkqaaa"
	return c.CreateRootStructure(ctx, arcs)
}

// Verify verifies a transcript under a root.
func (c *Client) Verify(ctx context.Context, req *httpapi.VerifyRequest) (*httpapi.VerifyResponse, error) {
	var resp httpapi.VerifyResponse
	if err := c.do(ctx, http.MethodPost, "/verify", nil, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetLineage returns one lineage record.
func (c *Client) GetLineage(ctx context.Context, root string) (*httpapi.LineageRecordResponse, error) {
	var resp httpapi.LineageRecordResponse
	if err := c.do(ctx, http.MethodGet, "/lineage/"+url.PathEscape(root), nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// LineageAncestors returns ancestor roots.
func (c *Client) LineageAncestors(ctx context.Context, root string, maxDepth int) ([]string, error) {
	query := map[string]string{}
	if maxDepth > 0 {
		query["max_depth"] = strconv.Itoa(maxDepth)
	}
	var resp httpapi.CIDListResponse
	if err := c.do(ctx, http.MethodGet, "/lineage/"+url.PathEscape(root)+"/ancestors", query, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// LineageDescendants returns direct descendant roots.
func (c *Client) LineageDescendants(ctx context.Context, root string) ([]string, error) {
	var resp httpapi.CIDListResponse
	if err := c.do(ctx, http.MethodGet, "/lineage/"+url.PathEscape(root)+"/descendants", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Items, nil
}

// ListLineage returns all lineage records.
func (c *Client) ListLineage(ctx context.Context) ([]httpapi.LineageRecordResponse, error) {
	var resp httpapi.LineageListResponse
	if err := c.do(ctx, http.MethodGet, "/lineage", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Records, nil
}

// CountLineage returns the lineage record count.
func (c *Client) CountLineage(ctx context.Context) (int, error) {
	var resp httpapi.CountResponse
	if err := c.do(ctx, http.MethodGet, "/lineage/count", nil, nil, &resp); err != nil {
		return 0, err
	}
	return resp.Count, nil
}

func (c *Client) do(ctx context.Context, method string, route string, query map[string]string, body any, out any) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	u.Path = path.Join(u.Path, route)
	values := u.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	u.RawQuery = values.Encode()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		return &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}

	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) doRaw(ctx context.Context, method string, route string, query map[string]string, contentType string, body io.Reader, out any) error {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return err
	}
	u.Path = path.Join(u.Path, route)
	values := u.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	u.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			return &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		return &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}

	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
