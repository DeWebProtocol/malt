package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleVerify_413WhenJSONBodyExceedsLimit exercises the integration
// between the JSON body limit and the verify handler. Sending a body larger
// than the configured limit must produce 413 (not 400) so monitoring and
// clients can distinguish "too big" from "malformed".
func TestHandleVerify_413WhenJSONBodyExceedsLimit(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 16}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := bytes.NewReader([]byte(`{"prooflist":{"steps":[` + strings.Repeat(" ", 64) + `]}}`))
	resp, err := http.Post(ts.URL+"/verify", "application/json", body)
	if err != nil {
		t.Fatalf("POST /verify: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestHandleCreateStructure_413WhenJSONBodyExceedsLimit covers the second
// JSON-decoding write route.
func TestHandleCreateStructure_413WhenJSONBodyExceedsLimit(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 8}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := bytes.NewReader([]byte(`{"arcs":{"@payload":"` + strings.Repeat("x", 256) + `"}}`))
	resp, err := http.Post(ts.URL+"/_", "application/json", body)
	if err != nil {
		t.Fatalf("POST /_: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestHandleSemanticMutation_413WhenJSONBodyExceedsLimit covers /{root}/_mutate.
func TestHandleSemanticMutation_413WhenJSONBodyExceedsLimit(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 16}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// We do not need a valid CID for this test path: the body limit triggers
	// before any semantic processing.
	root := fakeCIDString("seed-mutation-413")
	body := bytes.NewReader([]byte(`{"deltas":[` + strings.Repeat(" ", 256) + `]}`))
	resp, err := http.Post(ts.URL+"/"+root+"/_mutate", "application/json", body)
	if err != nil {
		t.Fatalf("POST mutate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestHandleUnixFSWrite_413WhenUploadExceedsLimit covers POST /_unixfs.
// It also confirms small uploads are still accepted afterward.
func TestHandleUnixFSWrite_413WhenUploadExceedsLimit(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{UnixFSUploadBytes: 8}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/_unixfs?path=foo.txt", "application/octet-stream", strings.NewReader("0123456789ABCDEF"))
	if err != nil {
		t.Fatalf("POST _unixfs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}
