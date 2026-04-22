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
			Timeout: 30 * time.Second,
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

// CreateGraph creates a managed graph.
func (c *Client) CreateGraph(ctx context.Context, id string, backend string) (*httpapi.Graph, error) {
	req := &httpapi.GraphCreateRequest{ID: id, Backend: backend}
	var resp httpapi.GraphResponse
	if err := c.do(ctx, http.MethodPost, "/graphs", nil, req, &resp); err != nil {
		return nil, err
	}
	return resp.Graph, nil
}

// GetGraph returns graph metadata.
func (c *Client) GetGraph(ctx context.Context, id string) (*httpapi.Graph, error) {
	var resp httpapi.GraphResponse
	if err := c.do(ctx, http.MethodGet, "/graphs/"+url.PathEscape(id), nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Graph, nil
}

// ListGraphs returns all graphs.
func (c *Client) ListGraphs(ctx context.Context) ([]*httpapi.Graph, error) {
	var resp httpapi.GraphListResponse
	if err := c.do(ctx, http.MethodGet, "/graphs", nil, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Graphs, nil
}

// DeleteGraph deletes a managed graph.
func (c *Client) DeleteGraph(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/graphs/"+url.PathEscape(id), nil, nil, nil)
}

// FreezeGraph freezes a managed graph.
func (c *Client) FreezeGraph(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/graphs/"+url.PathEscape(id)+"/freeze", nil, map[string]any{}, nil)
}

// ResolveGraph resolves a path from a managed graph head.
func (c *Client) ResolveGraph(ctx context.Context, id string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/graphs/"+url.PathEscape(id)+"/resolve", p)
}

// ResolveRoot resolves a path from an explicit root.
func (c *Client) ResolveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/roots/"+url.PathEscape(root)+"/resolve", p)
}

// ProveGraph returns the transcript for a managed graph path.
func (c *Client) ProveGraph(ctx context.Context, id string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/graphs/"+url.PathEscape(id)+"/proof", p)
}

// ProveRoot returns the transcript for an explicit root path.
func (c *Client) ProveRoot(ctx context.Context, root string, p string) (*httpapi.ResolveResponse, error) {
	return c.resolve(ctx, "/roots/"+url.PathEscape(root)+"/proof", p)
}

// SnapshotGraph returns the managed graph head snapshot.
func (c *Client) SnapshotGraph(ctx context.Context, id string) (*httpapi.SnapshotResponse, error) {
	var resp httpapi.SnapshotResponse
	if err := c.do(ctx, http.MethodGet, "/graphs/"+url.PathEscape(id)+"/snapshot", nil, nil, &resp); err != nil {
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

// UpdateGraph updates a single path on a managed graph head.
func (c *Client) UpdateGraph(ctx context.Context, id string, path string, target string) (*httpapi.WriteUpdateResponse, error) {
	var resp httpapi.WriteUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/graphs/"+url.PathEscape(id)+"/update", map[string]string{"path": path}, &httpapi.UpdateRequest{Path: path, Target: target}, &resp); err != nil {
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

// BatchUpdateGraph performs a batch update on a managed graph head.
func (c *Client) BatchUpdateGraph(ctx context.Context, id string, updates map[string]string) (*httpapi.WriteBatchResponse, error) {
	var resp httpapi.WriteBatchResponse
	if err := c.do(ctx, http.MethodPost, "/graphs/"+url.PathEscape(id)+"/updates:batch", nil, &httpapi.BatchUpdateRequest{Updates: updates}, &resp); err != nil {
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

// CreateGraphStructure creates a structure and advances the managed graph head.
func (c *Client) CreateGraphStructure(ctx context.Context, id string, arcs map[string]string) (*httpapi.CreateStructureResponse, error) {
	var resp httpapi.CreateStructureResponse
	if err := c.do(ctx, http.MethodPost, "/graphs/"+url.PathEscape(id)+"/structure", nil, &httpapi.CreateStructureRequest{Arcs: arcs}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
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
