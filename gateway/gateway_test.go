package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/codec"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// testServer creates an httptest.Server backed by an in-memory MALT node.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()

	node, err := api.NewNode()
	if err != nil {
		t.Fatalf("failed to create MALT node: %v", err)
	}

	adapter := NewNodeAdapter(node)
	srv := NewServer(adapter, ":0")
	return httptest.NewServer(srv.Handler())
}

func fakeCID(seed string) string {
	mhash, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		// Fallback - should never happen with SHA2-256
		return "bafkreiaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	return cid.NewCidV1(cid.Raw, mhash).String()
}

func postJSON(url string, body string) (*http.Response, error) {
	return http.Post(url, "application/json", bytes.NewReader([]byte(body)))
}

func readJSON(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// ===== Graph Management Tests =====

func TestServer_GraphCreate(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := postJSON(srv.URL+"/graph", `{"id": "test-graph"}`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}

	var graphResp GraphResponse
	if err := readJSON(resp, &graphResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if graphResp.Graph.ID != "test-graph" {
		t.Errorf("expected graph id 'test-graph', got %q", graphResp.Graph.ID)
	}
}

func TestNodeAdapterEnsureGraphOpensManagedDefaultGraph(t *testing.T) {
	node, err := api.NewNode()
	if err != nil {
		t.Fatalf("failed to create MALT node: %v", err)
	}
	defer node.Close()

	if _, err := node.CreateManagedGraph(context.Background(), "default", "ipa"); err != nil {
		t.Fatalf("CreateManagedGraph failed: %v", err)
	}

	handler := NewServer(NewNodeAdapter(node), ":0").Handler()
	body := map[string]any{
		"arcs": map[string]string{
			"name": fakeCID("alice"),
		},
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/structure", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	root, err := cid.Decode(resp.Root)
	if err != nil {
		t.Fatalf("decode root: %v", err)
	}
	if got := codec.GetMaltCodec(root); got != codec.CodecMaltIPA {
		t.Fatalf("root codec = %x, want %x", got, codec.CodecMaltIPA)
	}
}

func TestServer_GraphCreate_Duplicate(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create first graph
	resp, err := postJSON(srv.URL+"/graph", `{"id": "dup-graph"}`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Try to create again with same id
	resp, err = postJSON(srv.URL+"/graph", `{"id": "dup-graph"}`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestServer_GraphCreate_MissingID(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := postJSON(srv.URL+"/graph", `{}`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestServer_GraphGet(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create graph first
	resp, err := postJSON(srv.URL+"/graph", `{"id": "get-graph"}`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Get the graph
	resp, err = http.Get(srv.URL + "/graph/get-graph")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var graphResp GraphResponse
	if err := readJSON(resp, &graphResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if graphResp.Graph.ID != "get-graph" {
		t.Errorf("expected 'get-graph', got %q", graphResp.Graph.ID)
	}
}

func TestServer_GraphGet_NotFound(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/graph/nonexistent")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestServer_GraphDelete(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create graph
	resp, err := postJSON(srv.URL+"/graph", `{"id": "del-graph"}`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Delete the graph
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/graph/del-graph", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify it's gone
	resp, err = http.Get(srv.URL + "/graph/del-graph")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestServer_GraphList(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Create two graphs
	for _, id := range []string{"list-1", "list-2"} {
		resp, err := postJSON(srv.URL+"/graph", `{"id": "`+id+`"}`)
		if err != nil {
			t.Fatalf("request failed for %s: %v", id, err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 for %s, got %d", id, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// List graphs
	resp, err := http.Get(srv.URL + "/graphs")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var graphResp GraphListResponse
	if err := readJSON(resp, &graphResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(graphResp.Graphs) != 2 {
		t.Errorf("expected 2 graphs, got %d", len(graphResp.Graphs))
	}
}

func TestServer_GraphScopedLifecycle(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := postJSON(srv.URL+"/graph", `{"id": "managed"}`)
	if err != nil {
		t.Fatalf("create graph failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	structBody := `{
		"arcs": {
			"name": "` + fakeCID("alice") + `",
			"age": "` + fakeCID("30") + `"
		}
	}`
	resp, err = postJSON(srv.URL+"/graph/managed/structure", structBody)
	if err != nil {
		t.Fatalf("create structure failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, bodyString(resp))
	}

	var createResp map[string]string
	if err := readJSON(resp, &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	initialRoot := createResp["root"]
	if initialRoot == "" {
		t.Fatal("expected non-empty root")
	}

	resp, err = http.Get(srv.URL + "/graph/managed")
	if err != nil {
		t.Fatalf("get graph failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var graphResp GraphResponse
	if err := readJSON(resp, &graphResp); err != nil {
		t.Fatalf("decode graph response: %v", err)
	}
	if graphResp.Graph.Root != initialRoot {
		t.Fatalf("graph root = %s, want %s", graphResp.Graph.Root, initialRoot)
	}
	if graphResp.Graph.ArcCount != 2 {
		t.Fatalf("arc count = %d, want 2", graphResp.Graph.ArcCount)
	}

	resp, err = http.Get(srv.URL + "/graph/managed/resolve/name")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var resolveResp ResolveResponse
	if err := readJSON(resp, &resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolveResp.Target != fakeCID("alice") {
		t.Fatalf("target = %s, want %s", resolveResp.Target, fakeCID("alice"))
	}

	resp, err = postJSON(srv.URL+"/graph/managed/update/name", `{"target":"`+fakeCID("bob")+`"}`)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, bodyString(resp))
	}

	var updateResp WriteUpdateResponse
	if err := readJSON(resp, &updateResp); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updateResp.NewRoot == initialRoot {
		t.Fatal("expected new root after managed update")
	}

	resp, err = http.Get(srv.URL + "/graph/managed")
	if err != nil {
		t.Fatalf("get graph failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if err := readJSON(resp, &graphResp); err != nil {
		t.Fatalf("decode graph response: %v", err)
	}
	if graphResp.Graph.Root != updateResp.NewRoot {
		t.Fatalf("graph root = %s, want %s", graphResp.Graph.Root, updateResp.NewRoot)
	}
	if graphResp.Graph.ArcCount != 2 {
		t.Fatalf("arc count = %d, want 2", graphResp.Graph.ArcCount)
	}

	resp, err = http.Get(srv.URL + "/graph/managed/resolve/name")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if err := readJSON(resp, &resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolveResp.Target != fakeCID("bob") {
		t.Fatalf("target = %s, want %s", resolveResp.Target, fakeCID("bob"))
	}
}

func TestServer_GraphScopedWriteRejectsFrozenGraph(t *testing.T) {
	node, err := api.NewNode()
	if err != nil {
		t.Fatalf("failed to create MALT node: %v", err)
	}
	defer node.Close()

	if _, err := node.CreateManagedGraph(context.Background(), "frozen", ""); err != nil {
		t.Fatalf("CreateManagedGraph failed: %v", err)
	}

	srv := httptest.NewServer(NewServer(NewNodeAdapter(node), ":0").Handler())
	defer srv.Close()

	resp, err := postJSON(srv.URL+"/graph/frozen/structure", `{"arcs":{"name":"`+fakeCID("alice")+`"}}`)
	if err != nil {
		t.Fatalf("create structure failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, bodyString(resp))
	}
	resp.Body.Close()

	if err := node.GraphManager().FreezeGraph(context.Background(), "frozen"); err != nil {
		t.Fatalf("FreezeGraph failed: %v", err)
	}

	resp, err = postJSON(srv.URL+"/graph/frozen/update/name", `{"target":"`+fakeCID("bob")+`"}`)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", resp.StatusCode, bodyString(resp))
	}
}

// ===== Health Test =====

func TestServer_Health(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var status map[string]string
	if err := readJSON(resp, &status); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if status["status"] != "ok" {
		t.Errorf("expected status 'ok', got %q", status["status"])
	}
}

// ===== Structure Creation and Snapshot Tests =====

func TestServer_CreateStructure(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	structBody := `{
		"arcs": {
			"data": "` + fakeCID("data-content") + `",
			"meta": "` + fakeCID("meta-content") + `"
		}
	}`
	resp, err := postJSON(srv.URL+"/structure", structBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", resp.StatusCode, bodyString(resp))
	}

	var result map[string]string
	if err := readJSON(resp, &result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if result["root"] == "" {
		t.Error("expected non-empty root CID")
	}
}

func TestServer_CreateStructure_EmptyArcs(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	resp, err := postJSON(srv.URL+"/structure", `{"arcs": {}}`)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestServer_Snapshot(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	structBody := `{
		"arcs": {
			"a": "` + fakeCID("data-a") + `",
			"b": "` + fakeCID("data-b") + `"
		}
	}`
	resp, err := postJSON(srv.URL+"/structure", structBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var createResp map[string]string
	if err := readJSON(resp, &createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	root := createResp["root"]

	// Get snapshot
	snapResp, err := http.Get(srv.URL + "/snapshot/" + root)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if snapResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", snapResp.StatusCode, bodyString(snapResp))
	}

	var snap SnapshotResponse
	if err := readJSON(snapResp, &snap); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(snap.Arcs) != 2 {
		t.Errorf("expected 2 arcs, got %d", len(snap.Arcs))
	}

	if snap.Root != root {
		t.Errorf("expected root %s, got %s", root, snap.Root)
	}
}

// ===== Arc Query Tests =====

func TestServer_Arc(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	structBody := `{
		"arcs": {
			"target": "` + fakeCID("target-data") + `"
		}
	}`
	resp, err := postJSON(srv.URL+"/structure", structBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var createResp map[string]string
	if err := readJSON(resp, &createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	root := createResp["root"]

	// Get arc
	arcResp, err := http.Get(srv.URL + "/arc/" + root + "/target")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if arcResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", arcResp.StatusCode, bodyString(arcResp))
	}

	var arc ArcResponse
	if err := readJSON(arcResp, &arc); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if arc.Path != "target" {
		t.Errorf("expected path 'target', got %q", arc.Path)
	}

	expectedTarget := fakeCID("target-data")
	if arc.Target != expectedTarget {
		t.Errorf("expected target %s, got %s", expectedTarget, arc.Target)
	}
}

func TestServer_Arc_NotFound(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	structBody := `{"arcs": {"a": "` + fakeCID("data-a") + `"}}`
	resp, err := postJSON(srv.URL+"/structure", structBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var createResp map[string]string
	if err := readJSON(resp, &createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	root := createResp["root"]

	// Query nonexistent arc
	arcResp, err := http.Get(srv.URL + "/arc/" + root + "/nonexistent")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if arcResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", arcResp.StatusCode)
	}
	arcResp.Body.Close()
}

// ===== Write-Side Tests =====

func TestServer_UpdateArc_Replace(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	structBody := `{
		"arcs": {
			"alpha": "` + fakeCID("v0-alpha") + `",
			"beta": "` + fakeCID("v0-beta") + `"
		}
	}`
	resp, err := postJSON(srv.URL+"/structure", structBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var createResp map[string]string
	if err := readJSON(resp, &createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	root := createResp["root"]

	// Update arc "alpha"
	updateBody := `{"target": "` + fakeCID("v1-alpha") + `"}`
	resp, err = postJSON(srv.URL+"/update/"+root+"/alpha", updateBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, bodyString(resp))
	}

	var updateResp WriteUpdateResponse
	if err := readJSON(resp, &updateResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if updateResp.Op != "replace" {
		t.Errorf("expected op 'replace', got %q", updateResp.Op)
	}

	if updateResp.NewRoot == updateResp.OldRoot {
		t.Error("newRoot should differ from oldRoot after update")
	}

	// Verify the arc was updated
	arcResp, err := http.Get(srv.URL + "/arc/" + updateResp.NewRoot + "/alpha")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if arcResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", arcResp.StatusCode)
	}

	var arc ArcResponse
	if err := readJSON(arcResp, &arc); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	expectedTarget := fakeCID("v1-alpha")
	if arc.Target != expectedTarget {
		t.Errorf("expected target %s, got %s", expectedTarget, arc.Target)
	}
}

func TestServer_UpdateArc_Insert(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	structBody := `{"arcs": {"a": "` + fakeCID("data-a") + `"}}`
	resp, err := postJSON(srv.URL+"/structure", structBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var createResp map[string]string
	if err := readJSON(resp, &createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	root := createResp["root"]

	// Insert new arc
	updateBody := `{"target": "` + fakeCID("data-new") + `"}`
	resp, err = postJSON(srv.URL+"/update/"+root+"/new-arc", updateBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, bodyString(resp))
	}

	var updateResp WriteUpdateResponse
	if err := readJSON(resp, &updateResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if updateResp.Op != "insert" {
		t.Errorf("expected op 'insert', got %q", updateResp.Op)
	}

	// Verify the new arc exists
	arcResp, err := http.Get(srv.URL + "/arc/" + updateResp.NewRoot + "/new-arc")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if arcResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", arcResp.StatusCode)
	}

	var arc ArcResponse
	if err := readJSON(arcResp, &arc); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	expectedTarget := fakeCID("data-new")
	if arc.Target != expectedTarget {
		t.Errorf("expected target %s, got %s", expectedTarget, arc.Target)
	}
}

func TestServer_BatchUpdate(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	structBody := `{
		"arcs": {
			"x": "` + fakeCID("v0-x") + `",
			"y": "` + fakeCID("v0-y") + `",
			"z": "` + fakeCID("v0-z") + `"
		}
	}`
	resp, err := postJSON(srv.URL+"/structure", structBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var createResp map[string]string
	if err := readJSON(resp, &createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	root := createResp["root"]

	// Batch update: replace "x", insert "w", delete "z"
	batchBody := `{
		"updates": {
			"x": "` + fakeCID("v1-x") + `",
			"w": "` + fakeCID("v0-w") + `",
			"z": ""
		}
	}`
	resp, err = postJSON(srv.URL+"/update/batch/"+root, batchBody)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, bodyString(resp))
	}

	var batchResp WriteBatchResponse
	if err := readJSON(resp, &batchResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(batchResp.PerArc) != 3 {
		t.Errorf("expected 3 per-arc results, got %d", len(batchResp.PerArc))
	}

	// Verify final state via snapshot
	newRoot := batchResp.NewRoot
	snapResp, err := http.Get(srv.URL + "/snapshot/" + newRoot)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if snapResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for snapshot, got %d", snapResp.StatusCode)
	}

	var snap SnapshotResponse
	if err := readJSON(snapResp, &snap); err != nil {
		t.Fatalf("failed to decode snapshot: %v", err)
	}

	// Check final state
	checks := map[string]string{
		"x": fakeCID("v1-x"),
		"y": fakeCID("v0-y"),
		"w": fakeCID("v0-w"),
	}
	for path, expected := range checks {
		got, ok := snap.Arcs[path]
		if !ok {
			t.Errorf("expected arc %q not found in snapshot", path)
			continue
		}
		if got != expected {
			t.Errorf("arc %q: expected %s, got %s", path, expected, got)
		}
	}

	// "z" should not exist
	if _, ok := snap.Arcs["z"]; ok {
		t.Error("expected 'z' to be deleted, but found in snapshot")
	}
}

func bodyString(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
