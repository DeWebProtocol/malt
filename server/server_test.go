package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	listsemantic "github.com/dewebprotocol/malt/auth/semantic/list"
	mappingsemantic "github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/layout/unixfs"
	"github.com/dewebprotocol/malt/runtime/node"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func requireProofListHeader(t *testing.T, resp *http.Response) prooflist.ProofList {
	t.Helper()
	proofListHeader := resp.Header.Get("X-Malt-ProofList")
	if proofListHeader == "" {
		t.Fatal("X-Malt-ProofList header is missing")
	}
	if got := resp.Header.Get("X-Malt-ProofList-Encoding"); got != "base64url-json" {
		t.Fatalf("X-Malt-ProofList-Encoding = %q, want %q", got, "base64url-json")
	}
	proofData, err := base64.RawURLEncoding.DecodeString(proofListHeader)
	if err != nil {
		t.Fatalf("decode proof list header: %v", err)
	}
	var pl prooflist.ProofList
	if err := json.Unmarshal(proofData, &pl); err != nil {
		t.Fatalf("unmarshal proof list: %v", err)
	}
	return pl
}

func TestServerHealthAndRootLifecycle(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var health httpapi.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("health status payload = %q, want %q", health.Status, "ok")
	}
	if health.LifecycleToken != "" {
		t.Fatalf("health lifecycle token = %q, want empty", health.LifecycleToken)
	}

	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"test": fakeCIDString("test")}),
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read root response: %v", err)
	}
	if bytes.Contains(body, []byte(`"root":"b"`)) {
		t.Fatalf("root response leaked cid.Undef serialization: %s", string(body))
	}

	var rootResp httpapi.CreateStructureResponse
	if err := json.Unmarshal(body, &rootResp); err != nil {
		t.Fatalf("decode root response: %v", err)
	}
	if rootResp.Root == "" {
		t.Fatalf("root = %q, want non-empty root", rootResp.Root)
	}
}

func TestServerHealthIncludesLifecycleTokenWhenConfigured(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0", WithLifecycleToken("managed-token")).Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	var health httpapi.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("health status payload = %q, want %q", health.Status, "ok")
	}
	if health.LifecycleToken != "managed-token" {
		t.Fatalf("health lifecycle token = %q, want managed-token", health.LifecycleToken)
	}
}

func TestFreshUnixFSWriteCreatesRoot(t *testing.T) {
	node := newTestNode(t)
	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/_unixfs?path=hello.txt", "application/octet-stream", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("POST /_unixfs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /_unixfs status = %d, want %d: %s", resp.StatusCode, http.StatusCreated, string(body))
	}

	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	if writeResp.OldRoot != "" {
		t.Fatalf("old root = %q, want empty", writeResp.OldRoot)
	}
	if writeResp.NewRoot == "" {
		t.Fatal("new root is empty")
	}

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/hello.txt")
	if err != nil {
		t.Fatalf("GET written file: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET written file status = %d, want %d: %s", resp.StatusCode, http.StatusOK, string(body))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("written file = %q, want %q", string(data), "hello")
	}
}

func TestServerUnixFSWriteRejectsLegacyRootWithoutMigrationOptIn(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}
	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	manifestCID, err := mockCAS.Put(t.Context(), []byte(`{"entries":["existing.txt"]}`))
	if err != nil {
		t.Fatalf("put legacy manifest: %v", err)
	}
	existingCID, err := mockCAS.Put(t.Context(), []byte("existing content"))
	if err != nil {
		t.Fatalf("put legacy file: %v", err)
	}
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload":     manifestCID.String(),
			"existing.txt": existingCID.String(),
		},
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create legacy root: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create legacy root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/"+createResp.Root+"/upload.txt", "application/octet-stream", strings.NewReader("new content"))
	if err != nil {
		t.Fatalf("write legacy root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("legacy root write status = %d, want %d: %s", resp.StatusCode, http.StatusConflict, string(body))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read error body: %v", err)
	}
	if !strings.Contains(string(body), "migrate=1") {
		t.Fatalf("legacy root write error = %q, want migrate opt-in hint", string(body))
	}
}

func TestServerCreateRootOnlyAcceptsUnderscoreRoute(t *testing.T) {
	node := newTestNode(t)
	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	body, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": fakeCIDString("name")}),
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}

	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /_: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /_ status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/not-a-create-route", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /not-a-create-route: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated {
		t.Fatalf("POST /not-a-create-route unexpectedly created a root")
	}
}

func TestServerGraphServiceUsesInjectedRuntime(t *testing.T) {
	srv := New(newTestNode(t), "127.0.0.1:0")
	injected := &stubGraphRuntime{id: "injected", namespace: "mock"}
	srv.defaultGraph = injected

	svc, err := srv.graphService(t.Context())
	if err != nil {
		t.Fatalf("graphService: %v", err)
	}
	if svc.runtime != injected {
		t.Fatalf("graphService runtime = %T, want injected runtime", svc.runtime)
	}
	if svc.runtime.ID() != "injected" || svc.runtime.Namespace() != "mock" {
		t.Fatalf("runtime identity = %q/%q", svc.runtime.ID(), svc.runtime.Namespace())
	}
}

var _ runtimeGraph = (*stubGraphRuntime)(nil)

type stubGraphRuntime struct {
	id        string
	namespace string
}

func (g *stubGraphRuntime) ID() string {
	return g.id
}

func (g *stubGraphRuntime) Namespace() string {
	return g.namespace
}

func (g *stubGraphRuntime) Resolver() graph.Resolver {
	return nil
}

func (g *stubGraphRuntime) Writer() graph.Writer {
	return nil
}

func (g *stubGraphRuntime) Semantic() mappingsemantic.Semantics {
	return nil
}

func (g *stubGraphRuntime) ListSemantic() listsemantic.Semantics {
	return nil
}

func TestServerLegacyGraphRoutesRemoved(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/graphs")
	if err != nil {
		t.Fatalf("legacy graphs request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy graphs status = %d, want 404 or 400", resp.StatusCode)
	}
}

func TestServerDeprecatedPublicRoutesRemoved(t *testing.T) {
	node := newTestNode(t)
	handler := New(node, "127.0.0.1:0").Handler()

	for _, tc := range []struct {
		method string
		route  string
		body   string
	}{
		{method: http.MethodGet, route: "/lineage"},
		{method: http.MethodGet, route: "/lineage/count"},
		{method: http.MethodPost, route: "/bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku/_batch-update", body: `{"updates":{"name":"bafkqaaa"}}`},
	} {
		req := httptest.NewRequest(tc.method, tc.route, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status = %d, want 404 or 405", tc.method, tc.route, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPut, "/bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku/path", strings.NewReader(`{"target":"bafkqaaa"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT update status = %d, want 404 or 405", rec.Code)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/lineage", strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s /lineage status = %d, want 404 tombstone", method, rec.Code)
		}
	}
}

func TestServerMetricsSnapshotAndResetEndpoints(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	rawData := []byte("metrics raw content")
	rawCID, err := mockCAS.Put(t.Context(), rawData)
	if err != nil {
		t.Fatalf("put raw content: %v", err)
	}

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": rawCID.String(),
			"file.txt": rawCID.String(),
		},
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var createStrResp httpapi.CreateStructureResponse
	// Need to decode the body before closing... re-read
	// Let's use a different approach: get the root from the response
	createBodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	_ = json.Unmarshal(createBodyBytes, &createStrResp)

	// Actually we need to redo since body was consumed
	createBody2, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": rawCID.String(),
			"file.txt": rawCID.String(),
		},
	})
	resp2, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody2))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp2.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp2.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp2.Body.Close()
	root := createResp.Root

	resp, err = http.Get(ts.URL + "/" + root + "/file.txt")
	if err != nil {
		t.Fatalf("content request failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(ts.URL + "/" + root + "/file.txt")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(ts.URL + "/" + root + "/file.txt")
	if err != nil {
		t.Fatalf("content proof request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content proof status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics request failed: %v", err)
	}
	var metricsResp httpapi.MetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&metricsResp); err != nil {
		t.Fatalf("decode metrics response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	snapshot := metricsResp.Snapshot
	if snapshot.CAS.PutCount != 1 {
		t.Fatalf("CAS PutCount = %d, want 1", snapshot.CAS.PutCount)
	}
	if snapshot.CAS.GetCount < 2 {
		t.Fatalf("CAS GetCount = %d, want at least 2", snapshot.CAS.GetCount)
	}
	if snapshot.CAS.BytesGet < uint64(len(rawData)*2) {
		t.Fatalf("CAS BytesGet = %d, want at least %d", snapshot.CAS.BytesGet, len(rawData)*2)
	}
	if snapshot.ArcTable.UpdateCount == 0 {
		t.Fatalf("ArcTable UpdateCount = %d, want > 0", snapshot.ArcTable.UpdateCount)
	}
	if snapshot.ArcTable.GetCount == 0 {
		t.Fatalf("ArcTable GetCount = %d, want > 0", snapshot.ArcTable.GetCount)
	}
	if snapshot.Proof.ProofListCount != 3 {
		t.Fatalf("ProofListCount = %d, want 3", snapshot.Proof.ProofListCount)
	}
	if snapshot.Proof.StepCount == 0 || snapshot.Proof.TotalBytes == 0 {
		t.Fatalf("Proof stats = %+v, want steps and byte accounting", snapshot.Proof)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/metrics:reset", nil)
	if err != nil {
		t.Fatalf("new reset request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reset request failed: %v", err)
	}
	var resetResp httpapi.MetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&resetResp); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if resetResp.Snapshot.CAS.GetCount != 0 || resetResp.Snapshot.ArcTable.UpdateCount != 0 || resetResp.Snapshot.Proof.ProofListCount != 0 {
		t.Fatalf("reset response snapshot = %+v, want zero counters", resetResp.Snapshot)
	}

	resp, err = http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics after reset request failed: %v", err)
	}
	var afterReset httpapi.MetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&afterReset); err != nil {
		t.Fatalf("decode metrics after reset response: %v", err)
	}
	resp.Body.Close()
	if afterReset.Snapshot.CAS.GetCount != 0 || afterReset.Snapshot.ArcTable.GetCount != 0 || afterReset.Snapshot.Proof.ProofListCount != 0 {
		t.Fatalf("metrics after reset = %+v, want zero counters", afterReset.Snapshot)
	}
}

func TestServerRootCreateResolveAndVerify(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	aliceData := []byte("alice")
	aliceCID, err := mockCAS.Put(t.Context(), aliceData)
	if err != nil {
		t.Fatalf("put alice content: %v", err)
	}
	target := aliceCID.String()
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": target}),
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}

	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create structure response: %v", err)
	}
	if createResp.Root == "" {
		t.Fatal("expected non-empty root")
	}

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+createResp.Root+"/name", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stat request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if got := resp.Header.Get("X-Malt-Key"); got != target {
		t.Fatalf("resolved target = %q, want %q", got, target)
	}

	// Verify the proof header returned by the default read endpoint.
	proofResp, err := http.Get(ts.URL + "/" + createResp.Root + "/name")
	if err != nil {
		t.Fatalf("proof request failed: %v", err)
	}
	defer proofResp.Body.Close()

	if proofResp.StatusCode != http.StatusOK {
		t.Fatalf("proof status = %d, want %d", proofResp.StatusCode, http.StatusOK)
	}

	_, _ = io.Copy(io.Discard, proofResp.Body)
	contentProof := requireProofListHeader(t, proofResp)
	proofTarget, err := contentProof.LastStepTarget()
	if err != nil {
		t.Fatalf("proof target: %v", err)
	}
	if proofTarget.String() != target {
		t.Fatalf("proof target = %q, want %q", proofTarget.String(), target)
	}
}

func TestServerResolvePrefixReturnsProofListByDefault(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	targetCID, err := mockCAS.Put(t.Context(), []byte("resolve proof target"))
	if err != nil {
		t.Fatalf("put target: %v", err)
	}
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": targetCID.String()}),
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create root: %v", err)
	}

	resp, err = http.Get(ts.URL + "/resolve/" + createResp.Root + "/name")
	if err != nil {
		t.Fatalf("resolve request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if payload["target"] != targetCID.String() {
		t.Fatalf("target = %v, want %s", payload["target"], targetCID.String())
	}
	if _, ok := payload["prooflist"].(map[string]any); !ok {
		t.Fatalf("prooflist missing from resolve response: %#v", payload)
	}
	if _, ok := payload["transcript"]; ok {
		t.Fatalf("transcript should not be exposed by resolve response: %#v", payload["transcript"])
	}
	if vary := resp.Header.Get("Vary"); !strings.Contains(vary, "X-Malt-Proof") {
		t.Fatalf("resolve Vary header = %q, want to contain X-Malt-Proof", vary)
	}

	resp, err = http.Get(ts.URL + "/resolve/" + createResp.Root + "/name?proof=false")
	if err != nil {
		t.Fatalf("resolve proof=false request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve proof=false status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var noProof map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&noProof); err != nil {
		t.Fatalf("decode proof=false resolve response: %v", err)
	}
	if noProof["target"] != targetCID.String() {
		t.Fatalf("proof=false target = %v, want %s", noProof["target"], targetCID.String())
	}
	if _, ok := noProof["prooflist"]; ok {
		t.Fatalf("prooflist should be absent when proof=false: %#v", noProof["prooflist"])
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/resolve/"+createResp.Root+"/name", nil)
	if err != nil {
		t.Fatalf("build resolve omit request: %v", err)
	}
	req.Header.Set("X-Malt-Proof", "omit")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("resolve omit request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve omit status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	noProof = nil
	if err := json.NewDecoder(resp.Body).Decode(&noProof); err != nil {
		t.Fatalf("decode omit resolve response: %v", err)
	}
	if _, ok := noProof["prooflist"]; ok {
		t.Fatalf("prooflist should be absent when X-Malt-Proof: omit: %#v", noProof["prooflist"])
	}
}

func TestServerContentRouteRejectsLegacyFormatModes(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	targetCID, err := mockCAS.Put(t.Context(), []byte("legacy format target"))
	if err != nil {
		t.Fatalf("put target: %v", err)
	}
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": targetCID.String()}),
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create root: %v", err)
	}

	for _, format := range []string{"resolve", "proof"} {
		resp, err := http.Get(ts.URL + "/" + createResp.Root + "/name?format=" + format)
		if err != nil {
			t.Fatalf("format=%s request: %v", format, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("format=%s status = %d, want %d", format, resp.StatusCode, http.StatusBadRequest)
		}
	}
}

func TestServerInvalidQueryPathsReturnBadRequest(t *testing.T) {
	node := newTestNode(t)
	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": fakeCIDString("name")}),
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create root: %v", err)
	}
	resp.Body.Close()

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "resolve parent", method: http.MethodGet, path: "/resolve/" + createResp.Root + "/.."},
		{name: "content duplicate slash", method: http.MethodGet, path: "/" + createResp.Root + "/docs//readme.txt"},
		{name: "content head current dir", method: http.MethodHead, path: "/" + createResp.Root + "/."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			req, err := http.NewRequest(tc.method, ts.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s %s status = %d, want %d", tc.method, tc.path, resp.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func TestServerVerifyAcceptsProofList(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	targetCID, err := mockCAS.Put(t.Context(), []byte("verify proof target"))
	if err != nil {
		t.Fatalf("put target: %v", err)
	}
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": targetCID.String()}),
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create root: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/resolve/" + createResp.Root + "/name")
	if err != nil {
		t.Fatalf("resolve request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	resp.Body.Close()
	if resolveResp.ProofList == nil {
		t.Fatal("resolve response missing ProofList")
	}

	verifyBody, err := json.Marshal(&httpapi.VerifyRequest{ProofList: *resolveResp.ProofList})
	if err != nil {
		t.Fatalf("marshal verify request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var verifyResp httpapi.VerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("decode verify response: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatal("expected ProofList to verify")
	}

	for _, legacyKind := range []string{"implicit", "hamt"} {
		t.Run("rejects "+legacyKind+" evidence", func(t *testing.T) {
			forged := *resolveResp.ProofList
			forged.Steps = append([]prooflist.Step(nil), resolveResp.ProofList.Steps...)
			forged.Steps[0].EvidenceKind = legacyKind

			verifyBody, err := json.Marshal(&httpapi.VerifyRequest{ProofList: forged})
			if err != nil {
				t.Fatalf("marshal legacy verify request: %v", err)
			}
			resp, err := http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
			if err != nil {
				t.Fatalf("legacy verify request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("legacy verify status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
			var errorResp httpapi.ErrorResponse
			if err := json.NewDecoder(resp.Body).Decode(&errorResp); err != nil {
				t.Fatalf("decode legacy verify error: %v", err)
			}
			if !strings.Contains(errorResp.Error, "server verifier supports explicit evidence only") {
				t.Fatalf("legacy verify error = %q, want explicit-only verifier message", errorResp.Error)
			}
		})
	}
}

func TestServerVerifyRejectsBranchingProofList(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	payloadCID, err := mockCAS.Put(t.Context(), []byte("verify branch payload"))
	if err != nil {
		t.Fatalf("put payload: %v", err)
	}
	aCID, err := mockCAS.Put(t.Context(), []byte("verify branch a"))
	if err != nil {
		t.Fatalf("put a: %v", err)
	}
	bCID, err := mockCAS.Put(t.Context(), []byte("verify branch b"))
	if err != nil {
		t.Fatalf("put b: %v", err)
	}
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": payloadCID.String(),
			"a":        aCID.String(),
			"b":        bCID.String(),
		},
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create root: %v", err)
	}
	resp.Body.Close()

	resolve := func(path string) prooflist.ProofList {
		t.Helper()
		resp, err := http.Get(ts.URL + "/resolve/" + createResp.Root + "/" + path)
		if err != nil {
			t.Fatalf("resolve %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("resolve %s status = %d, want %d", path, resp.StatusCode, http.StatusOK)
		}
		var resolveResp httpapi.ResolveResponse
		if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
			t.Fatalf("decode resolve %s: %v", path, err)
		}
		if resolveResp.ProofList == nil {
			t.Fatalf("resolve %s missing ProofList", path)
		}
		return *resolveResp.ProofList
	}

	aProof := resolve("a")
	bProof := resolve("b")
	verifyRejects := func(name string, pl prooflist.ProofList) {
		t.Helper()
		verifyBody, err := json.Marshal(&httpapi.VerifyRequest{ProofList: pl})
		if err != nil {
			t.Fatalf("marshal %s verify request: %v", name, err)
		}
		resp, err := http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
		if err != nil {
			t.Fatalf("verify %s prooflist: %v", name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var verifyResp httpapi.VerifyResponse
			if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
				t.Fatalf("decode %s verify response: %v", name, err)
			}
			if verifyResp.Valid {
				t.Fatalf("%s ProofList verified; want rejection", name)
			}
			return
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("verify %s proof status = %d, want %d or invalid response", name, resp.StatusCode, http.StatusBadRequest)
		}
	}

	mismatchedQuery := aProof
	mismatchedQuery.Query = "b"
	verifyRejects("query-mismatched", mismatchedQuery)

	forged := aProof
	forged.Query = "a/b"
	forged.Steps = append(append([]prooflist.Step(nil), aProof.Steps...), bProof.Steps...)
	verifyRejects("branching", forged)
}

func TestServerVerifyRejectsForgedPayloadBindingKind(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	targetCID, err := mockCAS.Put(t.Context(), []byte("verify forged payload binding"))
	if err != nil {
		t.Fatalf("put target: %v", err)
	}
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": targetCID.String()}),
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create root: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/resolve/" + createResp.Root + "/name")
	if err != nil {
		t.Fatalf("resolve name: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve name status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	resp.Body.Close()
	if resolveResp.ProofList == nil {
		t.Fatal("resolve response missing ProofList")
	}
	if len(resolveResp.ProofList.Steps) != 1 {
		t.Fatalf("prooflist steps = %d, want 1", len(resolveResp.ProofList.Steps))
	}
	if got := resolveResp.ProofList.Steps[0].Path; got != "name" {
		t.Fatalf("prooflist step path = %q, want name", got)
	}

	forged := *resolveResp.ProofList
	forged.Query = "@payload"
	forged.Steps = append([]prooflist.Step(nil), resolveResp.ProofList.Steps...)
	forged.Steps[0].Kind = prooflist.KindPayloadBinding

	verifyBody, err := json.Marshal(&httpapi.VerifyRequest{ProofList: forged})
	if err != nil {
		t.Fatalf("marshal verify request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify forged prooflist: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var verifyResp httpapi.VerifyResponse
		if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
			t.Fatalf("decode verify response: %v", err)
		}
		if verifyResp.Valid {
			t.Fatal("forged payload_binding kind verified; want rejection")
		}
		return
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("verify forged prooflist status = %d, want %d or invalid response", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestServerProofListReadEndpoints(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	targetData := []byte("prooflist-target")
	targetCID, err := mockCAS.Put(t.Context(), targetData)
	if err != nil {
		t.Fatalf("put target content: %v", err)
	}
	payloadData := []byte("prooflist-payload")
	payloadCID, err := mockCAS.Put(t.Context(), payloadData)
	if err != nil {
		t.Fatalf("put payload content: %v", err)
	}
	target := targetCID.String()
	payload := payloadCID.String()
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": payload,
			"name":     target,
		},
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/" + createResp.Root + "/name")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	proofResp := requireProofListHeader(t, resp)
	resp.Body.Close()
	proofTarget, err := proofResp.LastStepTarget()
	if err != nil {
		t.Fatalf("prooflist target: %v", err)
	}
	if proofTarget.String() != target {
		t.Fatalf("prooflist target = %q, want %q", proofTarget.String(), target)
	}
	if len(proofResp.Steps) == 0 {
		t.Fatal("expected non-empty prooflist")
	}

	rootPayloadData := []byte("root-prooflist-payload")
	rootPayloadCID, err := mockCAS.Put(t.Context(), rootPayloadData)
	if err != nil {
		t.Fatalf("put root payload: %v", err)
	}
	rootPayload := rootPayloadCID.String()
	rootCreateBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{
			"@payload": rootPayload,
		}),
	})
	if err != nil {
		t.Fatalf("marshal create root structure request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/_", "application/json", bytes.NewReader(rootCreateBody))
	if err != nil {
		t.Fatalf("create root structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var rootCreateResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&rootCreateResp); err != nil {
		t.Fatalf("decode root create response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/" + rootCreateResp.Root)
	if err != nil {
		t.Fatalf("root prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	rootProofResp := requireProofListHeader(t, resp)
	resp.Body.Close()
	rootProofTarget, err := rootProofResp.LastStepTarget()
	if err != nil {
		t.Fatalf("root prooflist target: %v", err)
	}
	if rootProofTarget.String() != rootPayload {
		t.Fatalf("root prooflist target = %q, want %q", rootProofTarget.String(), rootPayload)
	}
	if len(rootProofResp.Steps) != 1 {
		t.Fatalf("root prooflist steps = %d, want 1", len(rootProofResp.Steps))
	}
	if rootProofResp.Steps[0].Kind != prooflist.KindPayloadBinding {
		t.Fatalf("root prooflist step kind = %q, want %q", rootProofResp.Steps[0].Kind, prooflist.KindPayloadBinding)
	}
}

func TestServerManagedRootCreateCanonicalizesArcs(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	target := fakeCIDString("canonical-target")
	createStructureBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{
			"foo/bar":   target,
			"/foo//bar": target,
		}),
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}

	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createStructureBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.Root == "" {
		t.Fatal("expected non-empty root")
	}

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+createResp.Root+"/foo/bar", nil)
	statResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stat request failed: %v", err)
	}
	statResp.Body.Close()
	if statResp.StatusCode != http.StatusOK {
		t.Fatalf("stat status = %d, want %d", statResp.StatusCode, http.StatusOK)
	}
	if got := statResp.Header.Get("X-Malt-Key"); got != target {
		t.Fatalf("resolved target = %q, want %q", got, target)
	}
}

func TestServerSemanticMutationUpdatesRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	initialPayload := fakeCIDString("initial-payload")
	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": initialPayload,
			"name":     fakeCIDString("initial-name"),
		},
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	nextPayload := fakeCIDString("next-payload")
	nextName := fakeCIDString("next-name")
	mutationBody, _ := json.Marshal(&httpapi.SemanticMutationRequest{
		Deltas: []httpapi.SemanticMutationDelta{{
			Object: createResp.Root,
			Kind:   "map",
			Changes: []httpapi.SemanticMutationChange{
				{
					Path:   "@payload",
					Before: &httpapi.SemanticMutationTarget{Target: initialPayload},
					After:  &httpapi.SemanticMutationTarget{Target: nextPayload},
				},
				{
					Path:   "name",
					Before: &httpapi.SemanticMutationTarget{Target: fakeCIDString("initial-name")},
					After:  &httpapi.SemanticMutationTarget{Target: nextName},
				},
			},
		}},
	})
	resp, err = http.Post(ts.URL+"/"+createResp.Root+"/_mutate", "application/json", bytes.NewReader(mutationBody))
	if err != nil {
		t.Fatalf("semantic mutation request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("semantic mutation status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var mutationResp httpapi.SemanticMutationResponse
	if err := json.NewDecoder(resp.Body).Decode(&mutationResp); err != nil {
		t.Fatalf("decode semantic mutation response: %v", err)
	}
	resp.Body.Close()
	if mutationResp.BaseRoot != createResp.Root {
		t.Fatalf("base_root = %q, want %q", mutationResp.BaseRoot, createResp.Root)
	}
	if mutationResp.NewRoot == "" || mutationResp.NewRoot == createResp.Root {
		t.Fatalf("new_root = %q, want a new defined root", mutationResp.NewRoot)
	}
	if mutationResp.DeltaCount != 1 || mutationResp.ArcCount != 2 {
		t.Fatalf("receipt counts = deltas %d arcs %d, want 1/2", mutationResp.DeltaCount, mutationResp.ArcCount)
	}
	requireNoKVPrefix(t, node, "lineage:")
	requireNoKVPrefix(t, node, "children:")

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+mutationResp.NewRoot+"/name", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stat request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("X-Malt-Key"); got != nextName {
		t.Fatalf("resolved target = %q, want %q", got, nextName)
	}
}

func TestServerSemanticMutationRejectsInvalidRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": fakeCIDString("initial-name")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	index := uint64(0)
	tests := []struct {
		name string
		req  httpapi.SemanticMutationRequest
	}{
		{
			name: "list only root",
			req: httpapi.SemanticMutationRequest{
				Deltas: []httpapi.SemanticMutationDelta{{
					Object: createResp.Root,
					Kind:   "list",
					Changes: []httpapi.SemanticMutationChange{{
						Index: &index,
						After: &httpapi.SemanticMutationTarget{Target: fakeCIDString("chunk")},
					}},
				}},
			},
		},
		{
			name: "map old value mismatch",
			req: httpapi.SemanticMutationRequest{
				Deltas: []httpapi.SemanticMutationDelta{{
					Object: createResp.Root,
					Kind:   "map",
					Changes: []httpapi.SemanticMutationChange{{
						Path:   "name",
						Before: &httpapi.SemanticMutationTarget{Target: fakeCIDString("wrong-old-name")},
						After:  &httpapi.SemanticMutationTarget{Target: fakeCIDString("next-name")},
					}},
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(&tt.req)
			resp, err := http.Post(ts.URL+"/"+createResp.Root+"/_mutate", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("semantic mutation request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("semantic mutation status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}
		})
	}
}

func TestServerSemanticMutationRejectsLegacyPuts(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": fakeCIDString("initial-name")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	body, _ := json.Marshal(map[string]any{
		"puts": []map[string]any{{
			"object": createResp.Root,
			"kind":   "map",
			"entries": []map[string]string{{
				"path":   "name",
				"target": fakeCIDString("next-name"),
			}},
		}},
	})
	resp, err = http.Post(ts.URL+"/"+createResp.Root+"/_mutate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("legacy puts mutation request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("legacy puts status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestServerRootSemanticMutationMaterializesWithoutPublishingRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{
			"name": fakeCIDString("initial-name"),
		}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create root response: %v", err)
	}
	resp.Body.Close()

	nextName := fakeCIDString("root-next-name")
	mutationBody, _ := json.Marshal(map[string]any{
		"deltas": []map[string]any{{
			"object": createResp.Root,
			"kind":   "map",
			"changes": []map[string]any{
				{
					"path":   "@payload",
					"before": map[string]string{"target": fakeCIDString("payload")},
					"after":  map[string]string{"target": fakeCIDString("root-next-payload")},
				},
				{
					"path":   "name",
					"before": map[string]string{"target": fakeCIDString("initial-name")},
					"after":  map[string]string{"target": nextName},
				},
			},
		}},
	})
	resp, err = http.Post(ts.URL+"/"+createResp.Root+"/_mutate", "application/json", bytes.NewReader(mutationBody))
	if err != nil {
		t.Fatalf("root semantic mutation request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("root semantic mutation status = %d, want %d: %s", resp.StatusCode, http.StatusCreated, string(body))
	}
	var mutationResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&mutationResp); err != nil {
		t.Fatalf("decode root semantic mutation response: %v", err)
	}
	resp.Body.Close()
	if mutationResp["base_root"] != createResp.Root {
		t.Fatalf("base_root = %v, want %q", mutationResp["base_root"], createResp.Root)
	}
	newRoot, ok := mutationResp["new_root"].(string)
	if !ok || newRoot == "" || newRoot == createResp.Root {
		t.Fatalf("new_root = %v, want a new defined root", mutationResp["new_root"])
	}

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+newRoot+"/name", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stat request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("X-Malt-Key"); got != nextName {
		t.Fatalf("resolved target = %q, want %q", got, nextName)
	}
}

func TestServerUnixFSWritesPublishWriterReadableRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	resp, err = http.Post(ts.URL+"/"+root+"/docs?type=dir&migrate=1", "application/json", nil)
	if err != nil {
		t.Fatalf("create unixfs directory request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs directory status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var dirResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&dirResp); err != nil {
		t.Fatalf("decode unixfs directory response: %v", err)
	}
	resp.Body.Close()
	if dirResp.OldRoot != root {
		t.Fatalf("directory write old_root = %q, want %q", dirResp.OldRoot, root)
	}

	fileBody := []byte("hello from writer unixfs")
	resp, err = http.Post(ts.URL+"/"+root+"/docs/readme.txt?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write response: %v", err)
	}
	resp.Body.Close()
	if writeResp.Path != "docs/readme.txt" || writeResp.Kind != "file" {
		t.Fatalf("unexpected unixfs write response: %+v", writeResp)
	}
	if writeResp.NewRoot == "" || writeResp.ArcCount == 0 {
		t.Fatalf("unixfs write root=%q arc_count=%d, want defined", writeResp.NewRoot, writeResp.ArcCount)
	}

	rootCID, err := cid.Decode(writeResp.NewRoot)
	if err != nil {
		t.Fatalf("decode write root: %v", err)
	}
	if payload, err := node.ArcTable().Get(t.Context(), defaultRootGraphID, rootCID, arcset.CanonicalizePath("@payload")); err != nil || !payload.Defined() {
		t.Fatalf("root @payload from arctable = %s, err %v; want defined", payload, err)
	}

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/docs/readme.txt")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	contentProofResp := requireProofListHeader(t, resp)
	resp.Body.Close()
	if len(contentProofResp.Steps) == 0 {
		t.Fatalf("unexpected prooflist response: %+v", contentProofResp)
	}

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+writeResp.NewRoot+"/docs/readme.txt", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stat request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	statKind := resp.Header.Get("X-Malt-Kind")
	statSize := resp.Header.Get("Content-Length")
	if statKind != "file" || statSize != strconv.FormatInt(int64(len(fileBody)), 10) {
		t.Fatalf("unexpected stat: kind=%q size=%q", statKind, statSize)
	}

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/docs/readme.txt")
	if err != nil {
		t.Fatalf("content request failed: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, fileBody) {
		t.Fatalf("content status/body = %d %q, want %d %q", resp.StatusCode, string(got), http.StatusOK, string(fileBody))
	}

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/docs/readme.txt/extra")
	if err != nil {
		t.Fatalf("partial content path request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("partial content path status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestServerUnixFSIdempotentFileWriteReturnsSameRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	body := []byte("stable contents")
	resp, err = http.Post(ts.URL+"/"+createResp.Root+"/docs/readme.txt?migrate=1", "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("initial unixfs write failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("initial unixfs write status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var firstWrite httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&firstWrite); err != nil {
		t.Fatalf("decode initial write response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/"+firstWrite.NewRoot+"/docs/readme.txt", "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("idempotent unixfs write failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		errorBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("idempotent unixfs write status = %d, want %d: %s", resp.StatusCode, http.StatusCreated, string(errorBody))
	}
	var secondWrite httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&secondWrite); err != nil {
		t.Fatalf("decode idempotent write response: %v", err)
	}
	if secondWrite.NewRoot != firstWrite.NewRoot {
		t.Fatalf("idempotent write root = %q, want same root %q", secondWrite.NewRoot, firstWrite.NewRoot)
	}
	if secondWrite.ArcCount != 0 {
		t.Fatalf("idempotent write arc_count = %d, want 0", secondWrite.ArcCount)
	}
}

func TestServerResolveHeadAndReadShareUnixFSDirectoryTarget(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/"+createResp.Root+"/docs?type=dir&migrate=1", "application/json", nil)
	if err != nil {
		t.Fatalf("create unixfs directory request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs directory status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var dirResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&dirResp); err != nil {
		t.Fatalf("decode unixfs directory response: %v", err)
	}
	resp.Body.Close()

	headReq, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+dirResp.NewRoot+"/docs", nil)
	resp, err = http.DefaultClient.Do(headReq)
	if err != nil {
		t.Fatalf("directory HEAD request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("directory HEAD status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	headKey := resp.Header.Get("X-Malt-Key")
	if headKey == "" {
		t.Fatal("directory HEAD X-Malt-Key is missing")
	}
	headTarget := resp.Header.Get("X-Malt-Payload")
	if headTarget == "" {
		headTarget = headKey
	}

	resp, err = http.Get(ts.URL + "/resolve/" + dirResp.NewRoot + "/docs")
	if err != nil {
		t.Fatalf("directory resolve request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("directory resolve status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode directory resolve response: %v", err)
	}
	resp.Body.Close()
	if resolveResp.Target != headTarget {
		t.Fatalf("resolve target = %q, HEAD target = %q; want shared resolution target", resolveResp.Target, headTarget)
	}

	resp, err = http.Get(ts.URL + "/" + dirResp.NewRoot + "/docs")
	if err != nil {
		t.Fatalf("directory read request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("directory read status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	readProof := requireProofListHeader(t, resp)
	resp.Body.Close()
	readTarget, err := readProof.LastStepTarget()
	if err != nil {
		t.Fatalf("directory read proof target: %v", err)
	}
	if readTarget.String() != resolveResp.Target {
		t.Fatalf("read proof target = %q, resolve target = %q; want shared resolution target", readTarget.String(), resolveResp.Target)
	}
}

func TestServerHeadGetAndResolveSharePathResolution(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	fileBody := []byte("shared path target")
	resp, err = http.Post(ts.URL+"/"+createResp.Root+"/docs/nested/readme.txt?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write response: %v", err)
	}
	resp.Body.Close()

	for _, tc := range []struct {
		path string
		kind string
		body []byte
	}{
		{path: "docs", kind: "dir"},
		{path: "docs/nested/readme.txt", kind: "file", body: fileBody},
	} {
		t.Run(tc.path, func(t *testing.T) {
			headReq, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+writeResp.NewRoot+"/"+tc.path, nil)
			resp, err := http.DefaultClient.Do(headReq)
			if err != nil {
				t.Fatalf("HEAD request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("HEAD status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			if got := resp.Header.Get("X-Malt-Kind"); got != tc.kind {
				resp.Body.Close()
				t.Fatalf("HEAD kind = %q, want %q", got, tc.kind)
			}
			headTarget := resp.Header.Get("X-Malt-Payload")
			if headTarget == "" {
				headTarget = resp.Header.Get("X-Malt-Key")
			}
			resp.Body.Close()
			if headTarget == "" {
				t.Fatal("HEAD target headers are missing")
			}

			resp, err = http.Get(ts.URL + "/resolve/" + writeResp.NewRoot + "/" + tc.path)
			if err != nil {
				t.Fatalf("resolve request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("resolve status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			var resolveResp httpapi.ResolveResponse
			if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
				t.Fatalf("decode resolve response: %v", err)
			}
			resp.Body.Close()
			if resolveResp.Target != headTarget {
				t.Fatalf("resolve target = %q, HEAD target = %q; want shared path resolution", resolveResp.Target, headTarget)
			}

			resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/" + tc.path)
			if err != nil {
				t.Fatalf("GET request failed: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				t.Fatalf("GET status = %d, want %d", resp.StatusCode, http.StatusOK)
			}
			if tc.body != nil {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read GET body: %v", err)
				}
				if !bytes.Equal(body, tc.body) {
					t.Fatalf("GET body = %q, want %q", body, tc.body)
				}
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
			}
			readProof := requireProofListHeader(t, resp)
			resp.Body.Close()
			readTarget, err := readProof.LastStepTarget()
			if err != nil {
				t.Fatalf("GET proof target: %v", err)
			}
			if readTarget.String() != resolveResp.Target {
				t.Fatalf("GET proof target = %q, resolve target = %q; want shared path resolution", readTarget.String(), resolveResp.Target)
			}
		})
	}
}

func TestServerUnixFSWriteRootDoesNotSelfParent(t *testing.T) {
	node := newTestNode(t)
	arcs, ok := node.ArcTable().(interface {
		GetParent(context.Context, string, cid.Cid) (cid.Cid, error)
	})
	if !ok {
		t.Fatalf("test node ArcTable = %T, want parent lookup support", node.ArcTable())
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	resp, err = http.Post(ts.URL+"/"+root+"/readme.txt?migrate=1", "application/octet-stream", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("create unixfs file request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write response: %v", err)
	}
	resp.Body.Close()

	rootCID, err := cid.Decode(writeResp.NewRoot)
	if err != nil {
		t.Fatalf("decode unixfs write root: %v", err)
	}
	parent, err := arcs.GetParent(t.Context(), "demo", rootCID)
	if err != nil {
		t.Fatalf("read unixfs write root parent: %v", err)
	}
	if parent.Equals(rootCID) {
		t.Fatalf("unixfs write root self-parented: %s", rootCID)
	}
}

func TestServerManifestRootUsesManifestDirectoryPath(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}
	srv := New(node, "127.0.0.1:0")
	g, err := srv.getOrCreateGraph(t.Context())
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}

	manifestData := []byte(`{"entries":["large.bin","raw.txt"]}`)
	manifestCID, err := unixfs.NewDirectoryManifestCID(manifestData)
	if err != nil {
		t.Fatalf("NewDirectoryManifestCID: %v", err)
	}
	mockCAS.AddBlock(manifestCID, manifestData)

	stat, target, err := srv.statForResolvedKey(t.Context(), g, manifestCID)
	if err != nil {
		t.Fatalf("statForResolvedKey manifest: %v", err)
	}
	requireManifestStat(t, stat, manifestCID, []string{"large.bin", "raw.txt"})
	if !target.Equals(manifestCID) {
		t.Fatalf("stat target = %s, want manifest %s", target, manifestCID)
	}

	flat, err := srv.statFromFlatTarget(t.Context(), g, manifestCID)
	if err != nil {
		t.Fatalf("statFromFlatTarget manifest: %v", err)
	}
	requireManifestStat(t, flat, manifestCID, []string{"large.bin", "raw.txt"})

	legacy, err := srv.legacyPathStat(t.Context(), g, manifestCID, "")
	if err != nil {
		t.Fatalf("legacyPathStat manifest: %v", err)
	}
	requireManifestStat(t, legacy, manifestCID, []string{"large.bin", "raw.txt"})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/" + manifestCID.String())
	if err != nil {
		t.Fatalf("GET manifest root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET manifest status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read manifest body: %v", err)
	}
	if !bytes.Equal(got, manifestData) {
		t.Fatalf("GET manifest body = %q, want %q", got, manifestData)
	}
}

func requireManifestStat(t *testing.T, stat *httpapi.PathStatResponse, manifestCID cid.Cid, entries []string) {
	t.Helper()
	if stat == nil {
		t.Fatal("manifest stat is nil")
	}
	if stat.Kind != "dir" || stat.StorageKind != "manifest" {
		t.Fatalf("manifest stat kind/storage = %q/%q, want dir/manifest", stat.Kind, stat.StorageKind)
	}
	if stat.Key != manifestCID.String() || stat.Payload != manifestCID.String() {
		t.Fatalf("manifest key/payload = %q/%q, want %q", stat.Key, stat.Payload, manifestCID.String())
	}
	if strings.Join(stat.Entries, ",") != strings.Join(entries, ",") {
		t.Fatalf("manifest entries = %v, want %v", stat.Entries, entries)
	}
}

func TestServerStatAndContentContracts(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	// Prepare a raw file in CAS.
	rawData := []byte("hello raw")
	rawCID, _ := fakeCID(rawData)
	mockCAS.AddBlock(rawCID, rawData)

	// Create structure with all bindings (raw, list-backed, manifest).
	rootManifest := []byte(`{"entries":["large.bin","raw.txt"]}`)
	rootManifestCID, _ := fakeCID(rootManifest)
	mockCAS.AddBlock(rootManifestCID, rootManifest)

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload":  rootManifestCID.String(),
			"raw.txt":   rawCID.String(),
			"large.bin": rawCID.String(),
		},
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	_ = json.NewDecoder(resp.Body).Decode(&createResp)
	resp.Body.Close()

	root := createResp.Root

	// stat raw file
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+root+"/raw.txt", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stat raw: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat raw status = %d", resp.StatusCode)
	}
	rawKind := resp.Header.Get("X-Malt-Kind")
	rawStorage := resp.Header.Get("X-Malt-Storage-Kind")
	rawSize := resp.Header.Get("Content-Length")
	if rawKind != "file" || rawStorage != "raw" || rawSize != strconv.FormatInt(int64(len(rawData)), 10) {
		t.Fatalf("unexpected raw stat: kind=%q storage=%q size=%q", rawKind, rawStorage, rawSize)
	}

	// stat list file
	req, _ = http.NewRequest(http.MethodHead, ts.URL+"/"+root+"/large.bin", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stat list: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat list status = %d", resp.StatusCode)
	}
	listKind := resp.Header.Get("X-Malt-Kind")
	listStorage := resp.Header.Get("X-Malt-Storage-Kind")
	listSize := resp.Header.Get("Content-Length")
	if listKind != "file" || listSize != strconv.FormatInt(int64(len(rawData)), 10) {
		t.Fatalf("unexpected list stat: kind=%q storage=%q size=%q", listKind, listStorage, listSize)
	}

	// content raw full
	resp, err = http.Get(ts.URL + "/" + root + "/raw.txt")
	if err != nil {
		t.Fatalf("content raw: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != string(rawData) {
		t.Fatalf("unexpected raw content status/body: %d %q", resp.StatusCode, string(body))
	}

	// content raw range
	rangeReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+root+"/raw.txt", nil)
	rangeReq.Header.Set("Range", "bytes=0-4")
	resp, err = http.DefaultClient.Do(rangeReq)
	if err != nil {
		t.Fatalf("content raw range: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "hello" {
		t.Fatalf("unexpected raw range status/body: %d %q", resp.StatusCode, string(body))
	}

	// missing path => 404
	missingReq, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+root+"/missing", nil)
	resp, err = http.DefaultClient.Do(missingReq)
	if err != nil {
		t.Fatalf("stat missing: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing stat status = %d", resp.StatusCode)
	}
}

func TestServerDefaultGETSmallUnixFSFileIncludesPayloadProof(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	fileBody := []byte("hello content proof")
	resp, err = http.Post(ts.URL+"/"+root+"/docs/readme.txt?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/docs/readme.txt")
	if err != nil {
		t.Fatalf("raw content request: %v", err)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(rawBody, fileBody) {
		t.Fatalf("raw content status/body = %d %q, want %d %q", resp.StatusCode, rawBody, http.StatusOK, fileBody)
	}

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/docs/readme.txt")
	if err != nil {
		t.Fatalf("content proof request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content proof status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	proofBody, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(proofBody, fileBody) {
		t.Fatalf("content = %q, want %q", proofBody, fileBody)
	}
	if resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", resp.Header.Get("Accept-Ranges"))
	}
	proofResp := requireProofListHeader(t, resp)
	if proofResp.Query != "docs/readme.txt" {
		t.Fatalf("proof query = %q, want docs/readme.txt", proofResp.Query)
	}
	if err := proofResp.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("prooflist shape: %v", err)
	}
	if len(proofResp.Steps) == 0 {
		t.Fatal("expected prooflist steps")
	}
	last := proofResp.Steps[len(proofResp.Steps)-1]
	if last.Kind != prooflist.KindPayloadBinding || last.Path != "@payload" {
		t.Fatalf("last proof step = %q/%q, want payload binding @payload", last.Kind, last.Path)
	}
	verifyBody, err := json.Marshal(&httpapi.VerifyRequest{ProofList: proofResp})
	if err != nil {
		t.Fatalf("marshal verify request: %v", err)
	}
	verifyRespHTTP, err := http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify payload-binding prooflist request: %v", err)
	}
	defer verifyRespHTTP.Body.Close()
	if verifyRespHTTP.StatusCode != http.StatusOK {
		t.Fatalf("verify payload-binding prooflist status = %d, want %d", verifyRespHTTP.StatusCode, http.StatusOK)
	}
	var verifyResp httpapi.VerifyResponse
	if err := json.NewDecoder(verifyRespHTTP.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("decode verify payload-binding response: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatal("expected payload-binding prooflist verification to succeed")
	}
	for i, step := range proofResp.Steps {
		if step.Kind == prooflist.KindListIndex {
			t.Fatalf("small raw file included list-index step at %d: %+v", i, step)
		}
	}
}

func TestServerDefaultGETRangeIncludesMeasuredListRangeStep(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	fileBody := append(bytes.Repeat([]byte{'a'}, unixfs.DefaultChunkSize), []byte("bcdef")...)
	resp, err = http.Post(ts.URL+"/"+root+"/large.bin?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs large file: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs large file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write: %v", err)
	}
	resp.Body.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+writeResp.NewRoot+"/large.bin", nil)
	req.Header.Set("Range", "bytes=262142-262145")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("content proof range request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("content proof status = %d, want %d", resp.StatusCode, http.StatusPartialContent)
	}

	content, _ := io.ReadAll(resp.Body)
	if string(content) != "aabc" {
		t.Fatalf("content range = %q, want aabc", content)
	}
	wantContentRange := "bytes 262142-262145/262149"
	if got := resp.Header.Get("Content-Range"); got != wantContentRange {
		t.Fatalf("content range header = %q, want %q", got, wantContentRange)
	}
	proofResp := requireProofListHeader(t, resp)
	if err := proofResp.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("prooflist shape: %v", err)
	}

	var ranges []prooflist.Step
	for _, step := range proofResp.Steps {
		if step.Kind == prooflist.KindListRange {
			ranges = append(ranges, step)
		}
	}
	if len(ranges) != 1 {
		t.Fatalf("list-range steps = %d, want 1", len(ranges))
	}
	rangeStep := ranges[0]
	if rangeStep.Start == nil || *rangeStep.Start != 262142 {
		t.Fatalf("list-range start = %v, want 262142", rangeStep.Start)
	}
	if rangeStep.End == nil || *rangeStep.End != 262146 {
		t.Fatalf("list-range end = %v, want 262146", rangeStep.End)
	}
	if rangeStep.ChildCount == nil || *rangeStep.ChildCount != 2 {
		t.Fatalf("list-range child count = %v, want 2", rangeStep.ChildCount)
	}
	if rangeStep.TotalSize == nil || *rangeStep.TotalSize != uint64(len(fileBody)) {
		t.Fatalf("list-range total size = %v, want %d", rangeStep.TotalSize, len(fileBody))
	}
	if rangeStep.ChunkSize == nil || *rangeStep.ChunkSize != unixfs.DefaultChunkSize {
		t.Fatalf("list-range chunk size = %v, want %d", rangeStep.ChunkSize, unixfs.DefaultChunkSize)
	}
	if len(rangeStep.Segments) != 2 {
		t.Fatalf("list-range segments = %d, want 2", len(rangeStep.Segments))
	}
	if rangeStep.EvidenceBackend != "measured_list" {
		t.Fatalf("list-range evidence backend = %q, want measured_list", rangeStep.EvidenceBackend)
	}

	verifyBody, err := json.Marshal(&httpapi.VerifyRequest{ProofList: proofResp})
	if err != nil {
		t.Fatalf("marshal verify request: %v", err)
	}
	verifyRespHTTP, err := http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify list-range prooflist request: %v", err)
	}
	defer verifyRespHTTP.Body.Close()
	if verifyRespHTTP.StatusCode != http.StatusOK {
		t.Fatalf("verify list-range prooflist status = %d, want %d", verifyRespHTTP.StatusCode, http.StatusOK)
	}
	var verifyResp httpapi.VerifyResponse
	if err := json.NewDecoder(verifyRespHTTP.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("decode verify list-range response: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatal("expected list-range prooflist verification to succeed")
	}

	forgedTarget, err := fakeCID([]byte("forged measured list range target"))
	if err != nil {
		t.Fatalf("forge target cid: %v", err)
	}
	forgedProof := proofResp
	forgedProof.Steps = append([]prooflist.Step(nil), proofResp.Steps...)
	foundRange := false
	for i := range forgedProof.Steps {
		if forgedProof.Steps[i].Kind == prooflist.KindListRange {
			forgedProof.Steps[i].Target = forgedTarget
			foundRange = true
			break
		}
	}
	if !foundRange {
		t.Fatal("prooflist missing list-range step to forge")
	}
	verifyBody, err = json.Marshal(&httpapi.VerifyRequest{ProofList: forgedProof})
	if err != nil {
		t.Fatalf("marshal forged target verify request: %v", err)
	}
	verifyRespHTTP, err = http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify forged target list-range prooflist request: %v", err)
	}
	defer verifyRespHTTP.Body.Close()
	if verifyRespHTTP.StatusCode == http.StatusOK {
		if err := json.NewDecoder(verifyRespHTTP.Body).Decode(&verifyResp); err != nil {
			t.Fatalf("decode forged target verify response: %v", err)
		}
		if verifyResp.Valid {
			t.Fatal("expected forged list-range target prooflist verification to fail")
		}
	} else if verifyRespHTTP.StatusCode != http.StatusBadRequest {
		t.Fatalf("verify forged target prooflist status = %d, want %d or invalid response", verifyRespHTTP.StatusCode, http.StatusBadRequest)
	}

	openEndedProof := proofResp
	openEndedProof.Steps = append([]prooflist.Step(nil), proofResp.Steps...)
	for i := range openEndedProof.Steps {
		if openEndedProof.Steps[i].Kind == prooflist.KindListRange {
			openEndedProof.Steps[i].End = nil
		}
	}
	verifyBody, err = json.Marshal(&httpapi.VerifyRequest{ProofList: openEndedProof})
	if err != nil {
		t.Fatalf("marshal open-ended verify request: %v", err)
	}
	verifyRespHTTP, err = http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify open-ended list-range prooflist request: %v", err)
	}
	defer verifyRespHTTP.Body.Close()
	if verifyRespHTTP.StatusCode != http.StatusOK {
		t.Fatalf("verify open-ended list-range prooflist status = %d, want %d", verifyRespHTTP.StatusCode, http.StatusOK)
	}
	if err := json.NewDecoder(verifyRespHTTP.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("decode verify open-ended list-range response: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatal("expected open-ended list-range prooflist verification to succeed")
	}
}

func TestServerDefaultGETReturnsProofHeader(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	// Create a structure with a small raw file
	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	// Write a small file via UnixFS
	fileBody := []byte("hello proof header")
	resp, err = http.Post(ts.URL+"/"+root+"/readme.txt?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write: %v", err)
	}
	resp.Body.Close()

	// Default GET should include proof headers
	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/readme.txt")
	if err != nil {
		t.Fatalf("default GET request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default GET status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, fileBody) {
		t.Fatalf("default GET body = %q, want %q", string(body), string(fileBody))
	}

	proofListHeader := resp.Header.Get("X-Malt-ProofList")
	encodingHeader := resp.Header.Get("X-Malt-ProofList-Encoding")
	if proofListHeader == "" {
		t.Fatal("X-Malt-ProofList header is missing")
	}
	if encodingHeader != "base64url-json" {
		t.Fatalf("X-Malt-ProofList-Encoding = %q, want %q", encodingHeader, "base64url-json")
	}

	// Vary header should be present
	varyHeader := resp.Header.Get("Vary")
	if !strings.Contains(varyHeader, "X-Malt-Proof") {
		t.Fatalf("Vary header = %q, want to contain X-Malt-Proof", varyHeader)
	}

	// Decode and validate the proof list
	proofData, err := base64.RawURLEncoding.DecodeString(proofListHeader)
	if err != nil {
		t.Fatalf("decode proof list header: %v", err)
	}
	var pl prooflist.ProofList
	if err := json.Unmarshal(proofData, &pl); err != nil {
		t.Fatalf("unmarshal proof list: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("proof list validation: %v", err)
	}
}

func TestServerDefaultGETProofHeaderWithProofFalse(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	fileBody := []byte("opt-out proof")
	resp, err = http.Post(ts.URL+"/"+root+"/file.txt?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file: %v", err)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	resp.Body.Close()

	// GET with ?proof=false should omit proof headers
	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/file.txt?proof=false")
	if err != nil {
		t.Fatalf("GET with proof=false: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, fileBody) {
		t.Fatalf("GET body = %q, want %q", string(body), string(fileBody))
	}

	if resp.Header.Get("X-Malt-ProofList") != "" {
		t.Fatal("X-Malt-ProofList header should be absent when proof=false")
	}

	// Vary header should still be present since response varies based on X-Malt-Proof
	varyHeader := resp.Header.Get("Vary")
	if !strings.Contains(varyHeader, "X-Malt-Proof") {
		t.Fatalf("Vary header = %q, want to contain X-Malt-Proof", varyHeader)
	}
}

func TestServerDefaultGETProofHeaderWithXMaltProofOmit(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	fileBody := []byte("header opt-out proof")
	resp, err = http.Post(ts.URL+"/"+root+"/file2.txt?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file: %v", err)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	resp.Body.Close()

	// GET with X-Malt-Proof: omit should omit proof headers
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+writeResp.NewRoot+"/file2.txt", nil)
	req.Header.Set("X-Malt-Proof", "omit")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET with X-Malt-Proof: omit: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, fileBody) {
		t.Fatalf("GET body = %q, want %q", string(body), string(fileBody))
	}

	if resp.Header.Get("X-Malt-ProofList") != "" {
		t.Fatal("X-Malt-ProofList header should be absent when X-Malt-Proof: omit")
	}

	// Vary header should still be present since response varies based on X-Malt-Proof
	varyHeader := resp.Header.Get("Vary")
	if !strings.Contains(varyHeader, "X-Malt-Proof") {
		t.Fatalf("Vary header = %q, want to contain X-Malt-Proof", varyHeader)
	}
}

func TestServerDefaultGETDirectoryProofHeader(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	// Create a directory
	resp, err = http.Post(ts.URL+"/"+root+"/docs?type=dir&migrate=1", "application/json", nil)
	if err != nil {
		t.Fatalf("create directory: %v", err)
	}
	var dirResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&dirResp); err != nil {
		t.Fatalf("decode dir response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/"+dirResp.NewRoot+"/docs/readme.txt", "application/octet-stream", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("create nested file: %v", err)
	}
	var fileResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&fileResp); err != nil {
		t.Fatalf("decode file response: %v", err)
	}
	resp.Body.Close()

	// Default GET on directory should serve the directory manifest payload with a proof header.
	resp, err = http.Get(ts.URL + "/" + fileResp.NewRoot + "/docs")
	if err != nil {
		t.Fatalf("GET directory: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET directory status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read directory body: %v", err)
	}
	if got, want := string(body), `{"entries":["readme.txt"]}`; got != want {
		t.Fatalf("directory body = %q, want manifest %q", got, want)
	}

	proofListHeader := resp.Header.Get("X-Malt-ProofList")
	if proofListHeader == "" {
		t.Fatal("X-Malt-ProofList header is missing for directory GET")
	}

	// Vary header should be present
	varyHeader := resp.Header.Get("Vary")
	if !strings.Contains(varyHeader, "X-Malt-Proof") {
		t.Fatalf("Vary header = %q, want to contain X-Malt-Proof", varyHeader)
	}

	proofData, err := base64.RawURLEncoding.DecodeString(proofListHeader)
	if err != nil {
		t.Fatalf("decode proof list header: %v", err)
	}
	var pl prooflist.ProofList
	if err := json.Unmarshal(proofData, &pl); err != nil {
		t.Fatalf("unmarshal proof list: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("proof list validation: %v", err)
	}

	// Directory proofs should include a terminal @payload binding step
	var hasPayloadBinding bool
	for _, step := range pl.Steps {
		if step.Kind == prooflist.KindPayloadBinding && step.Path == "@payload" {
			hasPayloadBinding = true
			break
		}
	}
	if !hasPayloadBinding {
		t.Fatalf("directory proof missing terminal @payload binding step, steps: %+v", pl.Steps)
	}
}

func TestServerDefaultGETRangeProofHeader(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	// Create a large file that spans multiple chunks
	fileBody := append(bytes.Repeat([]byte{'a'}, unixfs.DefaultChunkSize), []byte("bcdef")...)
	resp, err = http.Post(ts.URL+"/"+root+"/large.bin?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs large file: %v", err)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	resp.Body.Close()

	// Range GET should include proof header with measured list range evidence.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+writeResp.NewRoot+"/large.bin", nil)
	req.Header.Set("Range", "bytes=262142-262145")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("range GET request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range GET status = %d, want %d (Partial Content)", resp.StatusCode, http.StatusPartialContent)
	}

	contentRange := resp.Header.Get("Content-Range")
	wantContentRange := "bytes 262142-262145/262149"
	if contentRange != wantContentRange {
		t.Fatalf("Content-Range = %q, want %q", contentRange, wantContentRange)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "aabc" {
		t.Fatalf("range GET body = %q, want %q", string(body), "aabc")
	}

	proofListHeader := resp.Header.Get("X-Malt-ProofList")
	if proofListHeader == "" {
		t.Fatal("X-Malt-ProofList header is missing for range GET")
	}

	// Vary header should be present
	varyHeader := resp.Header.Get("Vary")
	if !strings.Contains(varyHeader, "X-Malt-Proof") {
		t.Fatalf("Vary header = %q, want to contain X-Malt-Proof", varyHeader)
	}

	proofData, err := base64.RawURLEncoding.DecodeString(proofListHeader)
	if err != nil {
		t.Fatalf("decode proof list header: %v", err)
	}
	var pl prooflist.ProofList
	if err := json.Unmarshal(proofData, &pl); err != nil {
		t.Fatalf("unmarshal proof list: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("proof list validation: %v", err)
	}

	// Range GET proof should include one measured list-range step for the touched chunks.
	var rangeSteps []prooflist.Step
	for _, step := range pl.Steps {
		if step.Kind == prooflist.KindListRange {
			rangeSteps = append(rangeSteps, step)
		}
	}
	if len(rangeSteps) != 1 {
		t.Fatalf("list-range steps = %d, want 1", len(rangeSteps))
	}
	rangeStep := rangeSteps[0]
	if rangeStep.Start == nil || *rangeStep.Start != 262142 {
		t.Fatalf("list-range start = %v, want 262142", rangeStep.Start)
	}
	if rangeStep.End == nil || *rangeStep.End != 262146 {
		t.Fatalf("list-range end = %v, want 262146", rangeStep.End)
	}
	if len(rangeStep.Segments) != 2 {
		t.Fatalf("list-range segments = %d, want 2", len(rangeStep.Segments))
	}
}

func TestServerHEADDoesNotReturnProofHeaders(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()
	root := createResp.Root

	fileBody := []byte("head test file")
	resp, err = http.Post(ts.URL+"/"+root+"/head.txt?migrate=1", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file: %v", err)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	resp.Body.Close()

	// HEAD request should not return proof headers
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/"+writeResp.NewRoot+"/head.txt", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	if resp.Header.Get("X-Malt-ProofList") != "" {
		t.Fatal("X-Malt-ProofList header should be absent for HEAD request")
	}
	if resp.Header.Get("X-Malt-ProofList-Encoding") != "" {
		t.Fatal("X-Malt-ProofList-Encoding header should be absent for HEAD request")
	}
}

func newTestNode(t *testing.T) *node.Node {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "badger"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = "http://127.0.0.1:4318"

	n, err := node.NewNode(
		node.WithConfig(cfg),
		node.WithCAS(casmock.NewCAS()),
		// Tests below type-assert node.CAS() back to *casmock.CAS, so disable
		// the daemon's default CID-verifying wrapper for the mock reader.
		node.WithoutCASVerification(),
	)
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = n.Close()
	})
	return n
}

func fakeCIDString(seed string) string {
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum).String()
}

func withPayloadBinding(bindings map[string]string) map[string]string {
	out := make(map[string]string, len(bindings)+1)
	for path, target := range bindings {
		out[path] = target
	}
	if _, ok := out["@payload"]; !ok {
		out["@payload"] = fakeCIDString("payload")
	}
	return out
}

func fakeCID(data []byte) (cid.Cid, error) {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func requireNoKVPrefix(t *testing.T, node *node.Node, prefix string) {
	t.Helper()
	iter := node.KVStore().NewIterator(t.Context(), nil, nil)
	defer iter.Close()
	prefixBytes := []byte(prefix)
	for iter.Next() {
		if bytes.HasPrefix(iter.Key(), prefixBytes) {
			t.Fatalf("unexpected KV key with prefix %q: %s", prefix, string(iter.Key()))
		}
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("iterate KV keys: %v", err)
	}
}
