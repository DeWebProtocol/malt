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

// CreateBucket creates a managed bucket.
func (c *Client) CreateBucket(ctx context.Context, id string, backend string) (*httpapi.Bucket, error) {
	req := &httpapi.BucketCreateRequest{ID: id, Backend: backend}
	var resp httpapi.BucketResponse
	if err := c.do(ctx, http.MethodPost, "/buckets", nil, req, &resp); err != nil {
		return nil, err
	}
	return resp.Bucket, nil
}

// GetBucket returns bucket metadata.
func (c *Client) GetBucket(ctx context.Context, id string) (*httpapi.Bucket, error) {
	var resp httpapi.BucketResponse
	if err := c.do(ctx, http.MethodGet, "/buckets/"+url.PathEscape(id), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Bucket, nil
}

// ListBuckets returns all buckets.
func (c *Client) ListBuckets(ctx context.Context) ([]*httpapi.Bucket, error) {
	var resp httpapi.BucketListResponse
	if err := c.do(ctx, http.MethodGet, "/buckets", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Buckets, nil
}

// DeleteBucket deletes a managed bucket.
func (c *Client) DeleteBucket(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/buckets/"+url.PathEscape(id), nil, nil, nil)
}

// FreezeBucket freezes a managed bucket.
func (c *Client) FreezeBucket(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/freeze", nil, map[string]any{}, nil)
}

// ResolveBucket resolves a path from a managed bucket head.
func (c *Client) ResolveBucket(ctx context.Context, id string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/buckets/"+url.PathEscape(id)+"/resolve", p)
}

// ResolveRoot resolves a path from an explicit root.
func (c *Client) ResolveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/roots/"+url.PathEscape(root)+"/resolve", p)
}

// ProveBucket returns the transcript for a managed bucket path.
func (c *Client) ProveBucket(ctx context.Context, id string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/buckets/"+url.PathEscape(id)+"/proof", p)
}

// ProveRoot returns the transcript for an explicit root path.
func (c *Client) ProveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/roots/"+url.PathEscape(root)+"/proof", p)
}

// ProofListBucket returns a ProofList read result from a managed bucket head.
func (c *Client) ProofListBucket(ctx context.Context, id string, p string) (*httpapi.ProofListResponse, error) {
	return c.proofList(ctx, "/buckets/"+url.PathEscape(id)+"/prooflist", p)
}

// ProofListRoot returns a ProofList read result from an explicit root.
func (c *Client) ProofListRoot(ctx context.Context, root string, p string) (*httpapi.ProofListResponse, error) {
	return c.proofList(ctx, "/roots/"+url.PathEscape(root)+"/prooflist", p)
}

// SnapshotBucket returns the managed bucket head snapshot.
func (c *Client) SnapshotBucket(ctx context.Context, id string) (*httpapi.SnapshotResponse, error) {
	var resp httpapi.SnapshotResponse
	if err := c.do(ctx, http.MethodGet, "/buckets/"+url.PathEscape(id)+"/snapshot", nil, nil, &resp); err != nil {
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

// UpdateBucket updates a single path on a managed bucket head.
func (c *Client) UpdateBucket(ctx context.Context, id string, path string, target string) (*httpapi.WriteUpdateResponse, error) {
	var resp httpapi.WriteUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/update", map[string]string{"path": path}, &httpapi.UpdateRequest{Path: path, Target: target}, &resp); err != nil {
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

// BatchUpdateBucket performs a batch update on a managed bucket head.
func (c *Client) BatchUpdateBucket(ctx context.Context, id string, updates map[string]string) (*httpapi.WriteBatchResponse, error) {
	var resp httpapi.WriteBatchResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/updates:batch", nil, &httpapi.BatchUpdateRequest{Updates: updates}, &resp); err != nil {
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

// CreateBucketStructure creates a structure and advances the managed bucket head.
func (c *Client) CreateBucketStructure(ctx context.Context, id string, arcs map[string]string) (*httpapi.CreateStructureResponse, error) {
	var resp httpapi.CreateStructureResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/structure", nil, &httpapi.CreateStructureRequest{Arcs: arcs}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SetBucketHead sets the managed bucket head root.
func (c *Client) SetBucketHead(ctx context.Context, id string, newRoot string, arcCount int, expectedOldRoot string) error {
	req := &httpapi.BucketHeadSetRequest{
		NewRoot:         newRoot,
		ArcCount:        arcCount,
		ExpectedOldRoot: expectedOldRoot,
	}
	return c.do(ctx, http.MethodPut, "/buckets/"+url.PathEscape(id)+"/head", nil, req, nil)
}

// ApplyBucketSemanticMutation applies a gateway semantic mutation and advances the bucket head.
func (c *Client) ApplyBucketSemanticMutation(ctx context.Context, id string, req *httpapi.BucketSemanticMutationRequest) (*httpapi.BucketSemanticMutationResponse, error) {
	var resp httpapi.BucketSemanticMutationResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/semantic-mutations", nil, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) AddBucketUnixFSDirectory(ctx context.Context, id string, p string) (*httpapi.BucketUnixFSWriteResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.BucketUnixFSWriteResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/unixfs/directories", query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) AddBucketUnixFSFile(ctx context.Context, id string, p string, data []byte) (*httpapi.BucketUnixFSWriteResponse, error) {
	query := map[string]string{"path": p}
	var resp httpapi.BucketUnixFSWriteResponse
	if err := c.doRaw(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/unixfs/files", query, "application/octet-stream", bytes.NewReader(data), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) CreateBucketMap(ctx context.Context, id string, bindings map[string]string) (*httpapi.BucketMapCreateResponse, error) {
	var resp httpapi.BucketMapCreateResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/maps", nil, &httpapi.BucketMapCreateRequest{Bindings: bindings}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) SnapshotBucketMap(ctx context.Context, id string, root string) (*httpapi.BucketMapSnapshotResponse, error) {
	var resp httpapi.BucketMapSnapshotResponse
	if err := c.do(ctx, http.MethodGet, "/buckets/"+url.PathEscape(id)+"/maps/"+url.PathEscape(root)+"/snapshot", nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ResolveBucketMap(ctx context.Context, id string, root string, p string) (*httpapi.BucketMapResolveResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.BucketMapResolveResponse
	if err := c.do(ctx, http.MethodGet, "/buckets/"+url.PathEscape(id)+"/maps/"+url.PathEscape(root)+"/resolve", query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) UpdateBucketMap(ctx context.Context, id string, root string, path string, target string) (*httpapi.WriteUpdateResponse, error) {
	var resp httpapi.WriteUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/maps/"+url.PathEscape(root)+"/update", map[string]string{"path": path}, &httpapi.UpdateRequest{Path: path, Target: target}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) BatchUpdateBucketMap(ctx context.Context, id string, root string, updates map[string]string) (*httpapi.WriteBatchResponse, error) {
	var resp httpapi.WriteBatchResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/maps/"+url.PathEscape(root)+"/updates:batch", nil, &httpapi.BatchUpdateRequest{Updates: updates}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) CreateBucketList(ctx context.Context, id string, chunks []string, chunkSize int) (*httpapi.BucketListStatResponse, error) {
	var resp httpapi.BucketListStatResponse
	if err := c.do(ctx, http.MethodPost, "/buckets/"+url.PathEscape(id)+"/lists", nil, &httpapi.BucketListCreateRequest{Chunks: chunks, ChunkSize: chunkSize}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetBucketList(ctx context.Context, id string, root string) (*httpapi.BucketListStatResponse, error) {
	var resp httpapi.BucketListStatResponse
	if err := c.do(ctx, http.MethodGet, "/buckets/"+url.PathEscape(id)+"/lists/"+url.PathEscape(root), nil, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) StatBucketPath(ctx context.Context, id string, p string) (*httpapi.BucketStatResponse, error) {
	query := map[string]string{}
	if p != "" {
		query["path"] = p
	}
	var resp httpapi.BucketStatResponse
	if err := c.do(ctx, http.MethodGet, "/buckets/"+url.PathEscape(id)+"/stat", query, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetBucketContent(ctx context.Context, id string, p string, rangeHeader string) ([]byte, int, http.Header, error) {
	body, status, headers, err := c.OpenBucketContent(ctx, id, p, rangeHeader)
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

// GetBucketContentProof reads bucket content as JSON with range metadata and a
// ProofList for the same path/range.
func (c *Client) GetBucketContentProof(ctx context.Context, id string, p string, rangeHeader string) (*httpapi.BucketContentProofResponse, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "/buckets/"+url.PathEscape(id)+"/content:proof")
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

	var out httpapi.BucketContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OpenBucketContent opens a streaming response body for bucket content.
// Callers must close the returned ReadCloser.
func (c *Client) OpenBucketContent(ctx context.Context, id string, p string, rangeHeader string) (io.ReadCloser, int, http.Header, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, 0, nil, err
	}
	u.Path = path.Join(u.Path, "/buckets/"+url.PathEscape(id)+"/content")
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
