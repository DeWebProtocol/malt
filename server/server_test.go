package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestServerHealthAndGraphLifecycle(t *testing.T) {
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

	createBody, err := json.Marshal(&httpapi.GraphCreateRequest{ID: "demo"})
	if err != nil {
		t.Fatalf("marshal create graph request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/graphs", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create graph request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create graph status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var graphResp httpapi.GraphResponse
	if err := json.NewDecoder(resp.Body).Decode(&graphResp); err != nil {
		t.Fatalf("decode graph response: %v", err)
	}
	if graphResp.Graph == nil || graphResp.Graph.ID != "demo" {
		t.Fatalf("graph response = %+v, want id demo", graphResp.Graph)
	}
}

func TestServerRootCreateResolveAndVerify(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	target := fakeCIDString("alice")
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{"name": target},
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

	resp, err = http.Get(ts.URL + "/api/v1/roots/" + createResp.Root + "/resolve?path=name")
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
