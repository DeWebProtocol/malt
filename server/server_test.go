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

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

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

	resp, err = http.Get(ts.URL + "/" + root + "/file.txt?format=proof")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(ts.URL + "/" + root + "/file.txt?format=proof")
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

	// Verify using the proof endpoint
	proofResp, err := http.Get(ts.URL + "/" + createResp.Root + "/name?format=proof")
	if err != nil {
		t.Fatalf("proof request failed: %v", err)
	}
	defer proofResp.Body.Close()

	if proofResp.StatusCode != http.StatusOK {
		t.Fatalf("proof status = %d, want %d", proofResp.StatusCode, http.StatusOK)
	}

	var contentProof httpapi.ContentProofResponse
	if err := json.NewDecoder(proofResp.Body).Decode(&contentProof); err != nil {
		t.Fatalf("decode proof response: %v", err)
	}
	if contentProof.Key != target {
		t.Fatalf("proof target = %q, want %q", contentProof.Key, target)
	}
}

func TestServerResolveFormatReturnsProofListByDefault(t *testing.T) {
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

	resp, err = http.Get(ts.URL + "/" + createResp.Root + "/name?format=resolve")
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

	resp, err = http.Get(ts.URL + "/" + createResp.Root + "/name?format=resolve&proof=false")
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

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/"+createResp.Root+"/name?format=resolve", nil)
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

	resp, err = http.Get(ts.URL + "/" + createResp.Root + "/name?format=resolve")
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

	resp, err = http.Get(ts.URL + "/" + createResp.Root + "/name?format=proof")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var proofResp httpapi.ContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&proofResp); err != nil {
		t.Fatalf("decode prooflist response: %v", err)
	}
	resp.Body.Close()
	if proofResp.Key != target {
		t.Fatalf("prooflist target = %q, want %q", proofResp.Key, target)
	}
	if len(proofResp.ProofList.Steps) == 0 {
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

	resp, err = http.Get(ts.URL + "/" + rootCreateResp.Root + "/?format=proof")
	if err != nil {
		t.Fatalf("root prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var rootProofResp httpapi.ContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&rootProofResp); err != nil {
		t.Fatalf("decode root prooflist response: %v", err)
	}
	resp.Body.Close()
	if rootProofResp.Key != rootPayload {
		t.Fatalf("root prooflist target = %q, want %q", rootProofResp.Key, rootPayload)
	}
	if len(rootProofResp.ProofList.Steps) != 1 {
		t.Fatalf("root prooflist steps = %d, want 1", len(rootProofResp.ProofList.Steps))
	}
	if rootProofResp.ProofList.Steps[0].Kind != prooflist.KindPayloadBinding {
		t.Fatalf("root prooflist step kind = %q, want %q", rootProofResp.ProofList.Steps[0].Kind, prooflist.KindPayloadBinding)
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
		Puts: []httpapi.SemanticMutationPut{{
			Object: createResp.Root,
			Kind:   "map",
			Entries: []httpapi.SemanticMutationEntry{
				{Path: "@payload", Target: nextPayload},
				{Path: "name", Target: nextName},
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
	if mutationResp.PutCount != 1 || mutationResp.ArcCount != 2 {
		t.Fatalf("receipt counts = puts %d arcs %d, want 1/2", mutationResp.PutCount, mutationResp.ArcCount)
	}

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
				Puts: []httpapi.SemanticMutationPut{{
					Object: createResp.Root,
					Kind:   "list",
					Entries: []httpapi.SemanticMutationEntry{{
						Index:  &index,
						Target: fakeCIDString("chunk"),
					}},
				}},
			},
		},
		{
			name: "map missing payload",
			req: httpapi.SemanticMutationRequest{
				Puts: []httpapi.SemanticMutationPut{{
					Object: createResp.Root,
					Kind:   "map",
					Entries: []httpapi.SemanticMutationEntry{{
						Path:   "name",
						Target: fakeCIDString("next-name"),
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
		"puts": []map[string]any{{
			"object": createResp.Root,
			"kind":   "map",
			"entries": []map[string]string{
				{"path": "@payload", "target": fakeCIDString("root-next-payload")},
				{"path": "name", "target": nextName},
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

func TestServerUnixFSWritesPublishGatewayReadableRoot(t *testing.T) {
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

	resp, err = http.Post(ts.URL+"/"+root+"/docs?type=dir", "application/json", nil)
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

	fileBody := []byte("hello from gateway unixfs")
	resp, err = http.Post(ts.URL+"/"+root+"/docs/readme.txt", "application/octet-stream", bytes.NewReader(fileBody))
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

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/docs/readme.txt?format=proof")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var contentProofResp httpapi.ContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&contentProofResp); err != nil {
		t.Fatalf("decode prooflist response: %v", err)
	}
	resp.Body.Close()
	if contentProofResp.Key == "" || len(contentProofResp.ProofList.Steps) == 0 {
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
}

func TestServerUnixFSGatewayRootDoesNotSelfParent(t *testing.T) {
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

	resp, err = http.Post(ts.URL+"/"+root+"/readme.txt", "application/octet-stream", bytes.NewReader([]byte("hello")))
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
		t.Fatalf("read gateway root parent: %v", err)
	}
	if parent.Equals(rootCID) {
		t.Fatalf("gateway root self-parented: %s", rootCID)
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

func TestServerContentProofReadSmallUnixFSFileIncludesPayloadProof(t *testing.T) {
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
	resp, err = http.Post(ts.URL+"/"+root+"/docs/readme.txt", "application/octet-stream", bytes.NewReader(fileBody))
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

	resp, err = http.Get(ts.URL + "/" + writeResp.NewRoot + "/docs/readme.txt?format=proof")
	if err != nil {
		t.Fatalf("content proof request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content proof status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var proofResp httpapi.ContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&proofResp); err != nil {
		t.Fatalf("decode content proof response: %v", err)
	}
	if !bytes.Equal(proofResp.Content, fileBody) {
		t.Fatalf("content = %q, want %q", proofResp.Content, fileBody)
	}
	if proofResp.Range.StatusCode != http.StatusOK || proofResp.Range.ContentLength != int64(len(fileBody)) || proofResp.Range.TotalSize != int64(len(fileBody)) {
		t.Fatalf("unexpected range metadata: %+v", proofResp.Range)
	}
	if proofResp.Range.Partial || proofResp.Range.ContentRange != "" || proofResp.Range.AcceptRanges != "bytes" {
		t.Fatalf("unexpected full-read range metadata: %+v", proofResp.Range)
	}
	if proofResp.ProofList.Query != "docs/readme.txt" {
		t.Fatalf("proof query = %q, want docs/readme.txt", proofResp.ProofList.Query)
	}
	if err := proofResp.ProofList.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("prooflist shape: %v", err)
	}
	if len(proofResp.ProofList.Steps) == 0 {
		t.Fatal("expected prooflist steps")
	}
	last := proofResp.ProofList.Steps[len(proofResp.ProofList.Steps)-1]
	if last.Kind != prooflist.KindPayloadBinding || last.Path != "@payload" {
		t.Fatalf("last proof step = %q/%q, want payload binding @payload", last.Kind, last.Path)
	}
	for i, step := range proofResp.ProofList.Steps {
		if step.Kind == prooflist.KindListIndex {
			t.Fatalf("small raw file included list-index step at %d: %+v", i, step)
		}
	}
}

func TestServerContentProofReadUnixFSRangeIncludesTouchedListIndexes(t *testing.T) {
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

	fileBody := append(bytes.Repeat([]byte{'a'}, fixedListChunkSize), []byte("bcdef")...)
	resp, err = http.Post(ts.URL+"/"+root+"/large.bin", "application/octet-stream", bytes.NewReader(fileBody))
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

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+writeResp.NewRoot+"/large.bin?format=proof", nil)
	req.Header.Set("Range", "bytes=262142-262145")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("content proof range request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content proof status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var proofResp httpapi.ContentProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&proofResp); err != nil {
		t.Fatalf("decode content proof response: %v", err)
	}
	if string(proofResp.Content) != "aabc" {
		t.Fatalf("content range = %q, want aabc", proofResp.Content)
	}
	wantContentRange := "bytes 262142-262145/262149"
	if proofResp.Range.StatusCode != http.StatusPartialContent || proofResp.Range.ContentRange != wantContentRange {
		t.Fatalf("range metadata = %+v, want status 206 content-range %q", proofResp.Range, wantContentRange)
	}
	if proofResp.Range.Start != 262142 || proofResp.Range.EndExclusive != 262146 || proofResp.Range.ContentLength != 4 || !proofResp.Range.Partial {
		t.Fatalf("unexpected byte range metadata: %+v", proofResp.Range)
	}
	if err := proofResp.ProofList.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("prooflist shape: %v", err)
	}

	var indexes []uint64
	for _, step := range proofResp.ProofList.Steps {
		if step.Kind == prooflist.KindListIndex {
			if step.Index == nil {
				t.Fatalf("list-index step missing index: %+v", step)
			}
			if step.Length == nil || *step.Length != 2 {
				t.Fatalf("list-index step length = %v, want 2", step.Length)
			}
			indexes = append(indexes, *step.Index)
			if step.EvidenceBackend != "list" {
				t.Fatalf("list-index evidence backend = %q, want list", step.EvidenceBackend)
			}
		}
	}
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Fatalf("list-index steps = %v, want [0 1]", indexes)
	}

	verifyBody, err := json.Marshal(&httpapi.VerifyRequest{ProofList: proofResp.ProofList})
	if err != nil {
		t.Fatalf("marshal verify request: %v", err)
	}
	verifyRespHTTP, err := http.Post(ts.URL+"/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify list-index prooflist request: %v", err)
	}
	defer verifyRespHTTP.Body.Close()
	if verifyRespHTTP.StatusCode != http.StatusOK {
		t.Fatalf("verify list-index prooflist status = %d, want %d", verifyRespHTTP.StatusCode, http.StatusOK)
	}
	var verifyResp httpapi.VerifyResponse
	if err := json.NewDecoder(verifyRespHTTP.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("decode verify list-index response: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatal("expected list-index prooflist verification to succeed")
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
	resp, err = http.Post(ts.URL+"/"+root+"/readme.txt", "application/octet-stream", bytes.NewReader(fileBody))
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
	resp, err = http.Post(ts.URL+"/"+root+"/file.txt", "application/octet-stream", bytes.NewReader(fileBody))
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
	resp, err = http.Post(ts.URL+"/"+root+"/file2.txt", "application/octet-stream", bytes.NewReader(fileBody))
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
	resp, err = http.Post(ts.URL+"/"+root+"/docs?type=dir", "application/json", nil)
	if err != nil {
		t.Fatalf("create directory: %v", err)
	}
	var dirResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&dirResp); err != nil {
		t.Fatalf("decode dir response: %v", err)
	}
	resp.Body.Close()

	// Default GET on directory should include proof header
	resp, err = http.Get(ts.URL + "/" + dirResp.NewRoot + "/docs")
	if err != nil {
		t.Fatalf("GET directory: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET directory status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var dirStat httpapi.PathStatResponse
	if err := json.NewDecoder(resp.Body).Decode(&dirStat); err != nil {
		t.Fatalf("decode directory response: %v", err)
	}
	if dirStat.Kind != "dir" {
		t.Fatalf("dir stat kind = %q, want %q", dirStat.Kind, "dir")
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
	fileBody := append(bytes.Repeat([]byte{'a'}, fixedListChunkSize), []byte("bcdef")...)
	resp, err = http.Post(ts.URL+"/"+root+"/large.bin", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs large file: %v", err)
	}
	var writeResp httpapi.UnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	resp.Body.Close()

	// Range GET should include proof header with list index steps
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

	// Range GET proof should include list index steps for the touched chunks
	var indexes []uint64
	for _, step := range pl.Steps {
		if step.Kind == prooflist.KindListIndex {
			if step.Index == nil {
				t.Fatalf("list-index step missing index: %+v", step)
			}
			indexes = append(indexes, *step.Index)
		}
	}
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Fatalf("list-index steps = %v, want [0 1]", indexes)
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
	resp, err = http.Post(ts.URL+"/"+root+"/head.txt", "application/octet-stream", bytes.NewReader(fileBody))
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

func newTestNode(t *testing.T) *api.Node {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "badger"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "mock"

	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})
	return node
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
