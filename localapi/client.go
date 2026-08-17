// Package localapi is the transport-neutral client for the private MALT
// daemon control plane. GUI and CLI adapters can share it without duplicating
// trust, filesystem, or mount lifecycle behavior.
package localapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	cid "github.com/ipfs/go-cid"
)

const defaultBaseURL = "http://malt.local"

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Options struct {
	HTTPClient Doer
	BaseURL    string
}

type Client struct {
	http    Doer
	baseURL string
}

type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("MALT local API error (%d): %s", e.StatusCode, e.Message)
}

func New(opts Options) (*Client, error) {
	if opts.HTTPClient == nil {
		return nil, fmt.Errorf("local API HTTP client is nil")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("local API base URL must use http or https")
	}
	return &Client{http: opts.HTTPClient, baseURL: baseURL}, nil
}

func (c *Client) ListMounts(ctx context.Context) ([]filesystemmount.Status, error) {
	var response struct {
		Mounts []filesystemmount.Status `json:"mounts"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/mounts", http.StatusOK, nil, &response); err != nil {
		return nil, err
	}
	if response.Mounts == nil {
		return nil, fmt.Errorf("local API mount list is missing")
	}
	seenIDs := make(map[string]struct{}, len(response.Mounts))
	seenMountpoints := make(map[string]struct{}, len(response.Mounts))
	for index := range response.Mounts {
		if err := validateStatus(response.Mounts[index], nil, false); err != nil {
			return nil, fmt.Errorf("local API mount list entry %d: %w", index, err)
		}
		if _, exists := seenIDs[response.Mounts[index].Spec.ID]; exists {
			return nil, fmt.Errorf("local API mount list contains duplicate ID %q", response.Mounts[index].Spec.ID)
		}
		if _, exists := seenMountpoints[response.Mounts[index].Spec.Mountpoint]; exists {
			return nil, fmt.Errorf("local API mount list contains duplicate mountpoint %q", response.Mounts[index].Spec.Mountpoint)
		}
		seenIDs[response.Mounts[index].Spec.ID] = struct{}{}
		seenMountpoints[response.Mounts[index].Spec.Mountpoint] = struct{}{}
	}
	return response.Mounts, nil
}

func (c *Client) Mount(ctx context.Context, spec filesystemmount.Spec) (filesystemmount.Status, error) {
	expected, err := filesystemmount.NormalizeSpec(spec)
	if err != nil {
		return filesystemmount.Status{}, err
	}
	var status filesystemmount.Status
	if err := c.do(ctx, http.MethodPost, "/v1/mounts", http.StatusCreated, expected, &status); err != nil {
		return filesystemmount.Status{}, err
	}
	if err := validateStatus(status, &expected, true); err != nil {
		return filesystemmount.Status{}, err
	}
	return status, nil
}

func (c *Client) Unmount(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || strings.Contains(id, "/") {
		return fmt.Errorf("mount ID is empty, reserved, or contains a slash")
	}
	return c.do(ctx, http.MethodDelete, "/v1/mounts/"+url.PathEscape(id), http.StatusNoContent, nil, nil)
}

func (c *Client) do(ctx context.Context, method, route string, expectedStatus int, requestBody, responseBody any) error {
	if c == nil || c.http == nil {
		return fmt.Errorf("local API client is nil")
	}
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+route, body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return fmt.Errorf("local API response exceeds 4 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Message string `json:"error"`
		}
		if json.Unmarshal(data, &failure) != nil || strings.TrimSpace(failure.Message) == "" {
			failure.Message = strings.TrimSpace(string(data))
		}
		if failure.Message == "" {
			failure.Message = response.Status
		}
		return &Error{StatusCode: response.StatusCode, Message: failure.Message}
	}
	if response.StatusCode != expectedStatus {
		return fmt.Errorf("local API returned status %d, want %d", response.StatusCode, expectedStatus)
	}
	if responseBody == nil {
		if len(bytes.TrimSpace(data)) != 0 {
			return fmt.Errorf("local API no-content response contains a body")
		}
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fmt.Errorf("local API response has unsupported Content-Type %q", response.Header.Get("Content-Type"))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(responseBody); err != nil {
		return fmt.Errorf("decode local API response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode local API response: expected one JSON value")
	}
	return nil
}

func validateStatus(status filesystemmount.Status, expected *filesystemmount.Spec, requireActive bool) error {
	normalized, err := filesystemmount.NormalizeSpec(status.Spec)
	if err != nil {
		return fmt.Errorf("local API returned an invalid mount specification: %w", err)
	}
	if normalized != status.Spec {
		return fmt.Errorf("local API returned a non-canonical mount specification")
	}
	if expected != nil && status.Spec != *expected {
		return fmt.Errorf("local API returned a mount for a different identity")
	}
	if strings.TrimSpace(status.Adapter) == "" || strings.TrimSpace(status.Adapter) != status.Adapter {
		return fmt.Errorf("local API returned an invalid mount adapter")
	}
	if status.Active {
		root, err := cid.Parse(status.SelectedRoot)
		if err != nil || root.String() != status.SelectedRoot {
			return fmt.Errorf("local API returned an active mount without a canonical selected root")
		}
	} else if status.SelectedRoot != "" || status.Revision != 0 {
		return fmt.Errorf("local API returned selected-root metadata for an inactive mount")
	}
	if requireActive && (!status.Desired || !status.Active) {
		return fmt.Errorf("local API mount response is not desired and active")
	}
	return nil
}
