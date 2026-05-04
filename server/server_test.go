package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

	resp, err := http.Get(ts.URL + "/api/v1/health")
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
	resp, err = http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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

func TestServerLegacyGraphRoutesRemoved(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/graphs")
	if err != nil {
		t.Fatalf("legacy graphs request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy graphs status = %d, want %d", resp.StatusCode, http.StatusNotFound)
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
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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
	resp2, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody2))
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

	resp, err = http.Get(ts.URL + "/api/v1/content/" + root + "/file.txt")
	if err != nil {
		t.Fatalf("content request failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(ts.URL + "/api/v1/prooflist/" + root + "/file.txt")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(ts.URL + "/api/v1/content-proof/" + root + "/file.txt")
	if err != nil {
		t.Fatalf("content proof request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("content proof status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	resp, err = http.Get(ts.URL + "/api/v1/metrics")
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
	if snapshot.Proof.ProofListCount != 2 {
		t.Fatalf("ProofListCount = %d, want 2", snapshot.Proof.ProofListCount)
	}
	if snapshot.Proof.StepCount == 0 || snapshot.Proof.TotalBytes == 0 {
		t.Fatalf("Proof stats = %+v, want steps and byte accounting", snapshot.Proof)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/metrics:reset", nil)
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

	resp, err = http.Get(ts.URL + "/api/v1/metrics")
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

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	target := fakeCIDString("alice")
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": target}),
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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

	resp, err = http.Get(ts.URL + "/api/v1/resolve/" + createResp.Root + "/name")
	if err != nil {
		t.Fatalf("resolve request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolveResp.Target != target {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, target)
	}

	verifyBody, err := json.Marshal(&httpapi.VerifyRequest{
		Root:       createResp.Root,
		Transcript: transcriptToVerifySteps(resolveResp.Transcript),
	})
	if err != nil {
		t.Fatalf("marshal verify request: %v", err)
	}

	resp, err = http.Post(ts.URL+"/api/v1/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify request failed: %v", err)
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
		t.Fatal("expected transcript verification to succeed")
	}
}

func TestServerProofListReadEndpoints(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	target := fakeCIDString("prooflist-target")
	payload := fakeCIDString("prooflist-payload")
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": payload,
			"name":     target,
		},
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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

	resp, err = http.Get(ts.URL + "/api/v1/prooflist/" + createResp.Root + "/name")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var listResp httpapi.ProofListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode prooflist response: %v", err)
	}
	resp.Body.Close()
	if listResp.Target != target {
		t.Fatalf("prooflist target = %q, want %q", listResp.Target, target)
	}
	if len(listResp.ProofList.Steps) == 0 {
		t.Fatal("expected non-empty prooflist")
	}

	rootPayload := fakeCIDString("root-prooflist-payload")
	rootCreateBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{
			"@payload": rootPayload,
		}),
	})
	if err != nil {
		t.Fatalf("marshal create root structure request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(rootCreateBody))
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

	resp, err = http.Get(ts.URL + "/api/v1/prooflist/" + rootCreateResp.Root + "/")
	if err != nil {
		t.Fatalf("root prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var rootResp httpapi.ProofListResponse
	if err := json.NewDecoder(resp.Body).Decode(&rootResp); err != nil {
		t.Fatalf("decode root prooflist response: %v", err)
	}
	resp.Body.Close()
	if rootResp.Target != rootPayload {
		t.Fatalf("root prooflist target = %q, want %q", rootResp.Target, rootPayload)
	}
	if len(rootResp.ProofList.Steps) != 1 {
		t.Fatalf("root prooflist steps = %d, want 1", len(rootResp.ProofList.Steps))
	}
	if rootResp.ProofList.Steps[0].Kind != prooflist.KindPayloadBinding {
		t.Fatalf("root prooflist step kind = %q, want %q", rootResp.ProofList.Steps[0].Kind, prooflist.KindPayloadBinding)
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

	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createStructureBody))
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

	resolveResp, err := http.Get(ts.URL + "/api/v1/resolve/" + createResp.Root + "/foo/bar")
	if err != nil {
		t.Fatalf("resolve request failed: %v", err)
	}
	defer resolveResp.Body.Close()
	if resolveResp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want %d", resolveResp.StatusCode, http.StatusOK)
	}
	var resolveTarget httpapi.ResolveResponse
	if err := json.NewDecoder(resolveResp.Body).Decode(&resolveTarget); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolveTarget.Target != target {
		t.Fatalf("resolved target = %q, want %q", resolveTarget.Target, target)
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
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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
	resp, err = http.Post(ts.URL+"/api/v1/roots/"+createResp.Root+"/semantic-mutations", "application/json", bytes.NewReader(mutationBody))
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

	resp, err = http.Get(ts.URL + "/api/v1/resolve/" + mutationResp.NewRoot + "/name")
	if err != nil {
		t.Fatalf("resolve request failed: %v", err)
	}
	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	resp.Body.Close()
	if resolveResp.Target != nextName {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, nextName)
	}
}

func TestServerSemanticMutationRejectsInvalidRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": fakeCIDString("initial-name")}),
	})
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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
			resp, err := http.Post(ts.URL+"/api/v1/roots/"+createResp.Root+"/semantic-mutations", "application/json", bytes.NewReader(body))
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
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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
	resp, err = http.Post(ts.URL+"/api/v1/roots/"+createResp.Root+"/semantic-mutations", "application/json", bytes.NewReader(mutationBody))
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

	resp, err = http.Get(ts.URL + "/api/v1/resolve/" + newRoot + "/name")
	if err != nil {
		t.Fatalf("resolve root request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("resolve root status = %d, want %d: %s", resp.StatusCode, http.StatusOK, string(body))
	}
	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	resp.Body.Close()
	if resolveResp.Target != nextName {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, nextName)
	}
}

func TestServerUnixFSWritesPublishGatewayReadableRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"dummy": fakeCIDString("dummy")}),
	})
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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

	resp, err = http.Post(ts.URL+"/api/v1/roots/"+root+"/unixfs/directory/docs", "application/json", nil)
	if err != nil {
		t.Fatalf("create unixfs directory request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs directory status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	resp.Body.Close()

	fileBody := []byte("hello from gateway unixfs")
	resp, err = http.Post(ts.URL+"/api/v1/roots/"+root+"/unixfs/file/docs/readme.txt", "application/octet-stream", bytes.NewReader(fileBody))
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

	resp, err = http.Get(ts.URL + "/api/v1/prooflist/" + writeResp.NewRoot + "/docs/readme.txt")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var proofResp httpapi.ProofListResponse
	if err := json.NewDecoder(resp.Body).Decode(&proofResp); err != nil {
		t.Fatalf("decode prooflist response: %v", err)
	}
	resp.Body.Close()
	if proofResp.Target == "" || len(proofResp.ProofList.Steps) == 0 {
		t.Fatalf("unexpected prooflist response: %+v", proofResp)
	}

	resp, err = http.Get(ts.URL + "/api/v1/stat/" + writeResp.NewRoot + "/docs/readme.txt")
	if err != nil {
		t.Fatalf("stat request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var statResp httpapi.PathStatResponse
	if err := json.NewDecoder(resp.Body).Decode(&statResp); err != nil {
		t.Fatalf("decode stat response: %v", err)
	}
	resp.Body.Close()
	if statResp.Kind != "file" || statResp.Size == nil || *statResp.Size != int64(len(fileBody)) {
		t.Fatalf("unexpected stat response: %+v", statResp)
	}

	resp, err = http.Get(ts.URL + "/api/v1/content/" + writeResp.NewRoot + "/docs/readme.txt")
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
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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

	resp, err = http.Post(ts.URL+"/api/v1/roots/"+root+"/unixfs/file/readme.txt", "application/octet-stream", bytes.NewReader([]byte("hello")))
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
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	_ = json.NewDecoder(resp.Body).Decode(&createResp)
	resp.Body.Close()

	root := createResp.Root

	// stat raw file
	resp, err = http.Get(ts.URL + "/api/v1/stat/" + root + "/raw.txt")
	if err != nil {
		t.Fatalf("stat raw: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat raw status = %d", resp.StatusCode)
	}
	var rawStat httpapi.PathStatResponse
	_ = json.NewDecoder(resp.Body).Decode(&rawStat)
	resp.Body.Close()
	if rawStat.Kind != "file" || rawStat.StorageKind != "raw" || rawStat.Size == nil || *rawStat.Size != int64(len(rawData)) {
		t.Fatalf("unexpected raw stat: %+v", rawStat)
	}

	// stat list file
	resp, err = http.Get(ts.URL + "/api/v1/stat/" + root + "/large.bin")
	if err != nil {
		t.Fatalf("stat list: %v", err)
	}
	var listStat httpapi.PathStatResponse
	_ = json.NewDecoder(resp.Body).Decode(&listStat)
	resp.Body.Close()
	if listStat.Kind != "file" || listStat.StorageKind != "raw" || listStat.Size == nil || *listStat.Size != int64(len(rawData)) {
		t.Fatalf("unexpected list stat: %+v", listStat)
	}

	// content raw full
	resp, err = http.Get(ts.URL + "/api/v1/content/" + root + "/raw.txt")
	if err != nil {
		t.Fatalf("content raw: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != string(rawData) {
		t.Fatalf("unexpected raw content status/body: %d %q", resp.StatusCode, string(body))
	}

	// content raw range
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/content/"+root+"/raw.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("content raw range: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "hello" {
		t.Fatalf("unexpected raw range status/body: %d %q", resp.StatusCode, string(body))
	}

	// missing path => 404
	resp, err = http.Get(ts.URL + "/api/v1/stat/" + root + "/missing")
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
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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
	resp, err = http.Post(ts.URL+"/api/v1/roots/"+root+"/unixfs/file/docs/readme.txt", "application/octet-stream", bytes.NewReader(fileBody))
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

	resp, err = http.Get(ts.URL + "/api/v1/content/" + writeResp.NewRoot + "/docs/readme.txt")
	if err != nil {
		t.Fatalf("raw content request: %v", err)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(rawBody, fileBody) {
		t.Fatalf("raw content status/body = %d %q, want %d %q", resp.StatusCode, rawBody, http.StatusOK, fileBody)
	}

	resp, err = http.Get(ts.URL + "/api/v1/content-proof/" + writeResp.NewRoot + "/docs/readme.txt")
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
	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
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
	resp, err = http.Post(ts.URL+"/api/v1/roots/"+root+"/unixfs/file/large.bin", "application/octet-stream", bytes.NewReader(fileBody))
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

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/content-proof/"+writeResp.NewRoot+"/large.bin", nil)
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
			indexes = append(indexes, *step.Index)
			if step.EvidenceBackend != "list" {
				t.Fatalf("list-index evidence backend = %q, want list", step.EvidenceBackend)
			}
		}
	}
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 1 {
		t.Fatalf("list-index steps = %v, want [0 1]", indexes)
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

func transcriptToVerifySteps(steps []httpapi.StepEvidence) []httpapi.VerifyStep {
	out := make([]httpapi.VerifyStep, len(steps))
	for i, step := range steps {
		out[i] = httpapi.VerifyStep{
			Path:     step.Path,
			Target:   step.Target,
			Evidence: step.Evidence,
			Kind:     step.Kind,
		}
	}
	return out
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
