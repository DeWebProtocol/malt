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

// GetCurrentRoot returns the daemon-managed current root pointer.
func (c *Client) GetCurrentRoot(ctx context.Context) (*httpapi.CurrentRootResponse, error) {
	var resp httpapi.CurrentRootResponse
	if err := c.do(ctx, http.MethodGet, "/current/root", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResolveCurrent resolves a path from a current root.
func (c *Client) ResolveCurrent(ctx context.Context, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/current/resolve", p)
}

// ResolveRoot resolves a path from an explicit root.
func (c *Client) ResolveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/roots/"+url.PathEscape(root)+"/resolve", p)
}

// ProveCurrent returns the transcript for a current-root path.
func (c *Client) ProveCurrent(ctx context.Context, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/current/proof", p)
}

// ProveRoot returns the transcript for an explicit root path.
func (c *Client) ProveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/roots/"+url.PathEscape(root)+"/proof", p)
}

// ProofListCurrent returns a ProofList read result from a current root.
func (c *Client) ProofListCurrent(ctx context.Context, p string) (*httpapi.ProofListResponse, error) {
	return c.proofList(ctx, "/current/prooflist", p)
}

// ProofListRoot returns a ProofList read result from an explicit root.
func (c *Client) ProofListRoot(ctx context.Context, root string, p string) (*httpapi.ProofListResponse, error) {
	return c.proofList(ctx, "/roots/"+url.PathEscape(root)+"/prooflist", p)
}

// SnapshotCurrent returns the current root snapshot.
func (c *Client) SnapshotCurrent(ctx context.Context) (*httpapi.SnapshotResponse, error) {
	var resp httpapi.SnapshotResponse
	if err := c.do(ctx, http.MethodGet, "/current/snapshot", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SnapshotRoot returns the snapshot for an explicit root.
func (c *Client) SnapshotRoot(ctx context.Context, root string) (*httpapi.SnapshotResponse, error) {
	var resp httpapi.SnapshotResponse
	if err := c.do(ctx, http.MethodGet, "/roots/"+url.PathEscape(root)+"/snapshot", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateCurrent updates a single path on a current root.
func (c *Client) UpdateCurrent(ctx context.Context, path string, target string) (*httpapi.WriteUpdateResponse, error) {
	var resp httpapi.WriteUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/current/update", map[string]string{"path": path}, &httpapi.UpdateRequest{Path: path, Target: target}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// UpdateRoot updates a single path under an explicit root.
func (c *Client) UpdateRoot(ctx context.Context, root string, path string, target string) (*httpapi.WriteUpdateResponse, error) {
	var resp httpapi.WriteUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/roots/"+url.PathEscape(root)+"/update", map[string]string{"path": path}, &httpapi.UpdateRequest{Path: path, Target: target}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BatchUpdateCurrent performs a batch update on a current root.
func (c *Client) BatchUpdateCurrent(ctx context.Context, updates map[string]string) (*httpapi.WriteBatchResponse, error) {
	var resp httpapi.WriteBatchResponse
	if err := c.do(ctx, http.MethodPost, "/current/updates:batch", nil, &httpapi.BatchUpdateRequest{Updates: updates}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BatchUpdateRoot performs a batch update under an explicit root.
func (c *Client) BatchUpdateRoot(ctx context.Context, root string, updates map[string]string) (*httpapi.WriteBatchResponse, error) {
	var resp httpapi.WriteBatchResponse
	if err := c.do(ctx, http.MethodPost, "/roots/"+url.PathEscape(root)+"/updates:batch", nil, &httpapi.BatchUpdateRequest{Updates: updates}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateCurrentStructure creates a structure and advances the current root.
func (c *Client) CreateCurrentStructure(ctx context.Context, arcs map[string]string) (*httpapi.CreateStructureResponse, error) {
	var resp httpapi.CreateStructureResponse
	if err := c.do(ctx, http.MethodPost, "/current/structure", nil, &httpapi.CreateStructureRequest{Arcs: arcs}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SetCurrentRoot sets the current root root.
func (c *Client) SetCurrentRoot(ctx context.Context, newRoot string, arcCount int, expectedOldRoot string) error {
	req := &httpapi.CurrentRootSetRequest{
		NewRoot:         newRoot,
		ArcCount:        arcCount,
		ExpectedOldRoot: expectedOldRoot,
	}
	return c.do(ctx, http.MethodPut, "/current/root", nil, req, nil)
}

// ApplyCurrentSemanticMutation applies a gateway semantic mutation and advances the current root head.
func (c *Client) ApplyCurrentSemanticMutation(ctx context.Context, req *httpapi.CurrentSemanticMutationRequest) (*httpapi.CurrentSemanticMutationResponse, error) {
	var resp httpapi.CurrentSemanticMutationResponse
	if err := c.do(ctx, http.MethodPost, "/current/semantic-mutations", nil, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ApplyRootSemanticMutation materializes a root-centric semantic mutation without publishing a current root head.
func (c *Client) ApplyRootSemanticMutation(ctx context.Context, root string, req *httpapi.RootSemanticMutationRequest) (*httpapi.RootSemanticMutationResponse, error) {
	var resp httpapi.RootSemanticMutationResponse
	if err := c.do(ctx, http.MethodPost, "/roots/"+url.PathEscape(root)+"/semantic-mutations", nil, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) AddCurrentUnixFSDirectory(ctx context.Context, p string) (*httpapi.UnixFSWriteResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.UnixFSWriteResponse
	if err := c.do(ctx, http.MethodPost, "/current/unixfs/directories", query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) AddCurrentUnixFSFile(ctx context.Context, p string, data []byte) (*httpapi.UnixFSWriteResponse, error) {
	query := map[string]string{"path": p}
	var resp httpapi.UnixFSWriteResponse
	if err := c.doRaw(ctx, http.MethodPost, "/current/unixfs/files", query, "application/octet-stream", bytes.NewReader(data), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ApplyCurrentUnixFSBatch(ctx context.Context, req *httpapi.UnixFSBatchRequest) (*httpapi.UnixFSBatchResponse, error) {
	var resp httpapi.UnixFSBatchResponse
	if err := c.do(ctx, http.MethodPost, "/current/unixfs:batch", nil, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) CreateCurrentMap(ctx context.Context, bindings map[string]string) (*httpapi.MapCreateResponse, error) {
	var resp httpapi.MapCreateResponse
	if err := c.do(ctx, http.MethodPost, "/current/maps", nil, &httpapi.MapCreateRequest{Bindings: bindings}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) SnapshotCurrentMap(ctx context.Context, root string) (*httpapi.MapSnapshotResponse, error) {
	var resp httpapi.MapSnapshotResponse
	if err := c.do(ctx, http.MethodGet, "/current/maps/"+url.PathEscape(root)+"/snapshot", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ResolveCurrentMap(ctx context.Context, root string, p string) (*httpapi.MapResolveResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.MapResolveResponse
	if err := c.do(ctx, http.MethodGet, "/current/maps/"+url.PathEscape(root)+"/resolve", query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) UpdateCurrentMap(ctx context.Context, root string, path string, target string) (*httpapi.WriteUpdateResponse, error) {
	var resp httpapi.WriteUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/current/maps/"+url.PathEscape(root)+"/update", map[string]string{"path": path}, &httpapi.UpdateRequest{Path: path, Target: target}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) BatchUpdateCurrentMap(ctx context.Context, root string, updates map[string]string) (*httpapi.WriteBatchResponse, error) {
	var resp httpapi.WriteBatchResponse
	if err := c.do(ctx, http.MethodPost, "/current/maps/"+url.PathEscape(root)+"/updates:batch", nil, &httpapi.BatchUpdateRequest{Updates: updates}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) CreateCurrentList(ctx context.Context, chunks []string, chunkSize int) (*httpapi.ListStatResponse, error) {
	var resp httpapi.ListStatResponse
	if err := c.do(ctx, http.MethodPost, "/current/lists", nil, &httpapi.ListCreateRequest{Chunks: chunks, ChunkSize: chunkSize}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetCurrentList(ctx context.Context, root string) (*httpapi.ListStatResponse, error) {
	var resp httpapi.ListStatResponse
	if err := c.do(ctx, http.MethodGet, "/current/lists/"+url.PathEscape(root), nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) StatCurrentPath(ctx context.Context, p string) (*httpapi.PathStatResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.PathStatResponse
	if err := c.do(ctx, http.MethodGet, "/current/stat", query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetCurrentContent(ctx context.Context, p string, rangeHeader string) ([]byte, int, http.Header, error) {
	body, status, headers, err := c.OpenCurrentContent(ctx, p, rangeHeader)
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

// GetCurrentContentProof reads current-root content as JSON with range metadata and a
// ProofList for the same path/range.
func (c *Client) GetCurrentContentProof(ctx context.Context, p string, rangeHeader string) (*httpapi.ContentProofResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "/current/content:proof")
	values := u.Query()
	if p != "" {
		values.Set("path", p)
	}
	u.RawQuery = values.Encode()

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

// OpenCurrentContent opens a streaming response body for current-root content.
// Callers must close the returned ReadCloser.
func (c *Client) OpenCurrentContent(ctx context.Context, p string, rangeHeader string) (io.ReadCloser, int, http.Header, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, 0, nil, err
	}
	u.Path = path.Join(u.Path, "/current/content")
	values := u.Query()
	if p != "" {
		values.Set("path", p)
	}
	u.RawQuery = values.Encode()

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
		var apiErr httpapi.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiErr); err == nil && apiErr.Error != "" {
			_ = resp.Body.Close()
			return nil, resp.StatusCode, resp.Header, &Error{StatusCode: resp.StatusCode, Message: apiErr.Error}
		}
		payload, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, resp.StatusCode, resp.Header, &Error{StatusCode: resp.StatusCode, Message: strings.TrimSpace(string(payload))}
	}
	return resp.Body, resp.StatusCode, resp.Header, nil
}

// CreateRootStructure creates a root-scoped structure.
func (c *Client) CreateRootStructure(ctx context.Context, arcs map[string]string) (*httpapi.CreateStructureResponse, error) {
	var resp httpapi.CreateStructureResponse
	if err := c.do(ctx, http.MethodPost, "/roots", nil, &httpapi.CreateStructureRequest{Arcs: arcs}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
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

func (c *Client) resolve(ctx context.Context, route string, p string) (*httpapi.ResolveResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.ResolveResponse
	if err := c.do(ctx, http.MethodGet, route, query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) proofList(ctx context.Context, route string, p string) (*httpapi.ProofListResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.ProofListResponse
	if err := c.do(ctx, http.MethodGet, route, query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
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
