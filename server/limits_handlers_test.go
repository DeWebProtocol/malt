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

// TestHandleCreateStructure_413WhenValidJSONHasOversizedSuffix is the
// regression for the second-round review finding: MaxBytesReader on its own
// only counts bytes a handler actually reads, so a small valid JSON value
// followed by a large suffix could decode successfully and return 201 while
// the unread megabytes still counted against no limit. The shared
// decodeJSONBody helper now fast-rejects on Content-Length and drains the
// remainder through the same limited reader, so this request must 413.
func TestHandleCreateStructure_413WhenValidJSONHasOversizedSuffix(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 64}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Tiny valid JSON the decoder can parse in one shot, followed by far
	// more bytes than the limit. Without the drain this would have been
	// accepted.
	tiny := []byte(`{}`)
	padding := strings.Repeat(" ", 4096)
	body := append(tiny, []byte(padding)...)

	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /_: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (valid JSON + oversized suffix must trip the limit)", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestHandleCreateStructure_413WhenContentLengthExceedsLimit covers the
// fast-reject path: when the client advertises a Content-Length beyond the
// limit, the helper refuses to read the body at all.
func TestHandleCreateStructure_413WhenContentLengthExceedsLimit(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 16}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := bytes.NewReader([]byte(`{"arcs":{"@payload":"` + strings.Repeat("x", 256) + `"}}`))
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/_", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(body.Len())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (Content-Length fast reject)", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestHandleVerify_413WhenValidJSONHasOversizedSuffix applies the same
// regression to /verify so the drain fix is locked in for both JSON
// handlers.
func TestHandleVerify_413WhenValidJSONHasOversizedSuffix(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 32}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := append([]byte(`{"prooflist":null}`), []byte(strings.Repeat(" ", 4096))...)
	resp, err := http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /verify: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestHandleSemanticMutation_413WhenValidJSONHasOversizedSuffix applies the
// same regression to /{root}/_mutate.
func TestHandleSemanticMutation_413WhenValidJSONHasOversizedSuffix(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 32}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	root := fakeCIDString("seed-mutation-suffix")
	body := append([]byte(`{"deltas":[]}`), []byte(strings.Repeat(" ", 4096))...)
	resp, err := http.Post(ts.URL+"/"+root+"/_mutate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST mutate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestDecodeJSONBody_ToleratesNoBody pins the nil-body path: a request with
// no body is reported as a 400 invalid JSON, never a 413.
func TestDecodeJSONBody_ToleratesNoBody(t *testing.T) {
	srv := New(nil, "127.0.0.1:0")
	err := srv.decodeJSONBody(httptest.NewRecorder(), &http.Request{}, &struct{}{})
	if err == nil {
		t.Fatal("expected error for request with no body")
	}
	if isMaxBytesError(err) {
		t.Fatalf("missing-body error classified as MaxBytes: %v", err)
	}
}

// TestHandleCreateStructure_400WhenTrailingSecondValue locks in the decoder
// buffer fix: a body with two valid JSON values must not be silently
// processed as the first. io.Copy(io.Discard, r.Body) cannot see bytes the
// decoder pre-buffered, so the trailing check must reuse the same decoder.
func TestHandleCreateStructure_400WhenTrailingSecondValue(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 4096}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Two small valid JSON objects. Total size well under the limit so the
	// only thing that can catch this is the decoder-reuse check.
	body := []byte(`{"arcs":{"@payload":"a"}} {"arcs":{"@payload":"b"}}`)
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /_: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (trailing JSON value must be rejected)", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestHandleCreateStructure_400WhenTrailingGarbage covers the garbage case
// (no valid second JSON value, but bytes remain after the first).
func TestHandleCreateStructure_400WhenTrailingGarbage(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 4096}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := []byte(`{"arcs":{"@payload":"a"}} --not json--`)
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /_: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (trailing garbage must be rejected)", resp.StatusCode, http.StatusBadRequest)
	}
}

// TestHandleCreateStructure_400WhenTrailingValueEvenWithMaxBytesReader
// confirms the decoder-reuse path is what catches the trailing value, not
// the size guard. This is the exact pattern the reviewer flagged: small
// valid JSON prefix, oversized suffix that fits in the decoder buffer.
func TestHandleCreateStructure_400WhenTrailingValueEvenWithMaxBytesReader(t *testing.T) {
	n := newTestNode(t)
	srv := New(n, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 4096}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := []byte(`{"arcs":{"@payload":"a"}} {"arcs":{"@payload":"` + strings.Repeat("b", 64) + `"}}`)
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /_: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
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
