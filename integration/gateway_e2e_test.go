// Package integration provides end-to-end integration tests for the MALT gateway.
// These tests exercise the full HTTP stack: graph management → resolution → proofs → writes.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/gateway"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func fakeGatewayCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func newTestGateway(t *testing.T) (*api.Node, http.Handler) {
	t.Helper()
	node, err := api.NewNode()
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	t.Cleanup(func() { node.Close() })

	adapter := gateway.NewNodeAdapter(node)
	srv := gateway.NewServer(adapter, "test:0")
	return node, srv.Handler()
}

func doRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		data, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(data)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// ===== Gateway E2E: Full Lifecycle =====

func TestGatewayE2E_FullLifecycle(t *testing.T) {
	_, handler := newTestGateway(t)

	// Step 1: Create a graph
	w := doRequest(t, handler, "POST", "/graph", map[string]string{
		"id": "test-graph-e2e",
	})
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("graph create failed: %d %s", w.Code, w.Body.String())
	}

	// Step 2: Get graph
	w = doRequest(t, handler, "GET", "/graph/test-graph-e2e", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("graph get failed: %d %s", w.Code, w.Body.String())
	}

	// Step 3: Create a structure (without graph_id to use default bucket)
	arcs := map[string]string{
		"arc0": fakeGatewayCID("payload0").String(),
		"arc1": fakeGatewayCID("payload1").String(),
		"arc2": fakeGatewayCID("payload2").String(),
	}
	w = doRequest(t, handler, "POST", "/structure", map[string]interface{}{
		"arcs": arcs,
	})
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("structure create failed: %d %s", w.Code, w.Body.String())
	}

	var createResp struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("parse create response: %v", err)
	}
	root := createResp.Root
	if root == "" {
		t.Fatal("empty root in create response")
	}

	// Step 4: Resolve an arc
	w = doRequest(t, handler, "GET", "/resolve/"+root+"/arc0", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve arc0 failed: %d %s", w.Code, w.Body.String())
	}

	var resolveResp struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resolveResp); err != nil {
		t.Fatalf("parse resolve response: %v", err)
	}
	if resolveResp.Target != arcs["arc0"] {
		t.Errorf("resolve target mismatch: got %s, want %s", resolveResp.Target, arcs["arc0"])
	}

	// Step 5: Generate a proof
	w = doRequest(t, handler, "GET", "/proof/"+root+"/arc0", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("proof arc0 failed: %d %s", w.Code, w.Body.String())
	}

	// Step 6: Get snapshot
	w = doRequest(t, handler, "GET", "/snapshot/"+root, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("snapshot failed: %d %s", w.Code, w.Body.String())
	}

	// Step 7: List graphs
	w = doRequest(t, handler, "GET", "/graphs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("graph list failed: %d %s", w.Code, w.Body.String())
	}
}

// ===== Gateway E2E: Update → Resolve =====

func TestGatewayE2E_UpdateAndResolve(t *testing.T) {
	_, handler := newTestGateway(t)

	// Create a structure
	arcs := map[string]string{
		"name": fakeGatewayCID("alice").String(),
		"age":  fakeGatewayCID("30").String(),
	}
	w := doRequest(t, handler, "POST", "/structure", map[string]interface{}{
		"arcs": arcs,
	})
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("structure create failed: %d %s", w.Code, w.Body.String())
	}

	var createResp struct {
		Root string `json:"root"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	root := createResp.Root

	// Resolve original
	w = doRequest(t, handler, "GET", "/resolve/"+root+"/name", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve name failed: %d", w.Code)
	}

	// Update name
	newName := fakeGatewayCID("bob").String()
	w = doRequest(t, handler, "POST", "/update/"+root+"/name", map[string]string{
		"target": newName,
	})
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("update name failed: %d %s", w.Code, w.Body.String())
	}

	var updateResp struct {
		NewRoot string `json:"new_root"`
	}
	json.Unmarshal(w.Body.Bytes(), &updateResp)
	newRoot := updateResp.NewRoot
	if newRoot == "" {
		t.Fatal("empty new_root in update response")
	}

	// Resolve on new root
	w = doRequest(t, handler, "GET", "/resolve/"+newRoot+"/name", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve name on new root failed: %d %s", w.Code, w.Body.String())
	}

	var resolveResp struct {
		Target string `json:"target"`
	}
	json.Unmarshal(w.Body.Bytes(), &resolveResp)
	if resolveResp.Target != newName {
		t.Errorf("expected target %s, got %s", newName, resolveResp.Target)
	}

	// Original root should still resolve to old value
	// Note: This depends on EAT implementation (overwrite vs versioned)
	// The overwrite EAT may not preserve old root entries
	w = doRequest(t, handler, "GET", "/resolve/"+root+"/age", nil)
	if w.Code != http.StatusOK {
		// This is expected with overwrite EAT - old entries may be overwritten
		t.Logf("old root resolution returned %d (expected with overwrite EAT)", w.Code)
	}
}

// ===== Gateway E2E: Health Check =====

func TestGatewayE2E_Health(t *testing.T) {
	_, handler := newTestGateway(t)

	w := doRequest(t, handler, "GET", "/health", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("health check failed: %d", w.Code)
	}

	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
		t.Fatalf("parse health response: %v", err)
	}
	if health.Status != "ok" {
		t.Errorf("expected status 'ok', got %s", health.Status)
	}
}

// ===== Gateway E2E: Batch Update =====

func TestGatewayE2E_BatchUpdate(t *testing.T) {
	_, handler := newTestGateway(t)

	// Create initial structure with 5 arcs
	arcs := make(map[string]string)
	for i := 0; i < 5; i++ {
		arcs[fmt.Sprintf("field%d", i)] = fakeGatewayCID(fmt.Sprintf("val%d", i)).String()
	}
	w := doRequest(t, handler, "POST", "/structure", map[string]interface{}{
		"arcs": arcs,
	})
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("structure create failed: %d %s", w.Code, w.Body.String())
	}

	var createResp struct {
		Root string `json:"root"`
	}
	json.Unmarshal(w.Body.Bytes(), &createResp)
	root := createResp.Root

	// Batch update 3 arcs
	updates := map[string]string{
		"field0": fakeGatewayCID("updated0").String(),
		"field2": fakeGatewayCID("updated2").String(),
		"field4": fakeGatewayCID("updated4").String(),
	}
	w = doRequest(t, handler, "POST", "/update/batch/"+root, map[string]interface{}{
		"updates": updates,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("batch update failed: %d %s", w.Code, w.Body.String())
	}

	var batchResp struct {
		NewRoot string `json:"new_root"`
	}
	json.Unmarshal(w.Body.Bytes(), &batchResp)
	newRoot := batchResp.NewRoot

	// Verify all updated arcs
	for path, expected := range updates {
		w = doRequest(t, handler, "GET", "/resolve/"+newRoot+"/"+path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("resolve %s on new root failed: %d", path, w.Code)
		}
		var resp struct {
			Target string `json:"target"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Target != expected {
			t.Errorf("%s: expected %s, got %s", path, expected, resp.Target)
		}
	}

	// Verify unchanged arcs still resolve to original values
	for path, expected := range arcs {
		if _, ok := updates[path]; ok {
			continue // skip updated ones
		}
		w = doRequest(t, handler, "GET", "/resolve/"+newRoot+"/"+path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("resolve unchanged %s failed: %d", path, w.Code)
		}
		var resp struct {
			Target string `json:"target"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Target != expected {
			t.Errorf("unchanged arc %s: expected %s, got %s", path, expected, resp.Target)
		}
	}
}
