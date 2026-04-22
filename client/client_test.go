package client

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/httpapi"
	"github.com/dewebprotocol/malt/server"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"net/http/httptest"
)

func TestClientGraphFlow(t *testing.T) {
	cfg := testConfig(t)
	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	cfg.RPC.Listen = ts.Listener.Addr().String()
	client := New(cfg)

	ctx := context.Background()
	graph, err := client.CreateGraph(ctx, "demo", "")
	if err != nil {
		t.Fatalf("create graph: %v", err)
	}
	if graph.ID != "demo" {
		t.Fatalf("graph id = %q, want %q", graph.ID, "demo")
	}
	loadedGraph, err := client.GetGraph(ctx, "demo")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	if loadedGraph.ID != "demo" {
		t.Fatalf("loaded graph id = %q, want %q", loadedGraph.ID, "demo")
	}

	target := fakeCIDString("alice")
	createResp, err := client.CreateRootStructure(ctx, map[string]string{"name": target})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}
	if createResp.Root == "" {
		t.Fatal("expected non-empty root")
	}

	resolveResp, err := client.ResolveRoot(ctx, createResp.Root, "name")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if resolveResp.Target != target {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, target)
	}

	verifyResp, err := client.Verify(ctx, &httpapi.VerifyRequest{
		Root:       createResp.Root,
		Transcript: toVerifySteps(resolveResp.Transcript),
	})
	if err != nil {
		t.Fatalf("verify transcript: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatal("expected verification to succeed")
	}

	updateTarget := fakeCIDString("bob")
	updateResp, err := client.UpdateRoot(ctx, createResp.Root, "name", updateTarget)
	if err != nil {
		t.Fatalf("update root: %v", err)
	}
	if updateResp.NewRoot == createResp.Root {
		t.Fatal("expected update to advance root")
	}

	snapshotResp, err := client.SnapshotRoot(ctx, updateResp.NewRoot)
	if err != nil {
		t.Fatalf("snapshot root: %v", err)
	}
	if snapshotResp.Arcs["name"] != updateTarget {
		t.Fatalf("snapshot target = %q, want %q", snapshotResp.Arcs["name"], updateTarget)
	}
}

func TestClientManagedGraphStructureFlow(t *testing.T) {
	cfg := testConfig(t)
	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	cfg.RPC.Listen = ts.Listener.Addr().String()
	client := New(cfg)
	ctx := context.Background()

	if _, err := client.CreateGraph(ctx, "demo", ""); err != nil {
		t.Fatalf("create graph: %v", err)
	}

	target := fakeCIDString("managed-alice")
	createResp, err := client.CreateGraphStructure(ctx, "demo", map[string]string{"name": target})
	if err != nil {
		t.Fatalf("create managed graph structure: %v", err)
	}
	if createResp.Root == "" {
		t.Fatal("expected non-empty managed graph root")
	}

	resolveResp, err := client.ResolveGraph(ctx, "demo", "name")
	if err != nil {
		t.Fatalf("resolve managed graph: %v", err)
	}
	if resolveResp.Target != target {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, target)
	}

	updateTarget := fakeCIDString("managed-bob")
	updateResp, err := client.UpdateGraph(ctx, "demo", "name", updateTarget)
	if err != nil {
		t.Fatalf("update managed graph: %v", err)
	}
	if updateResp.NewRoot == createResp.Root {
		t.Fatal("expected managed graph update to advance the head root")
	}

	snapshotResp, err := client.SnapshotGraph(ctx, "demo")
	if err != nil {
		t.Fatalf("snapshot managed graph: %v", err)
	}
	if snapshotResp.Arcs["name"] != updateTarget {
		t.Fatalf("snapshot target = %q, want %q", snapshotResp.Arcs["name"], updateTarget)
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	cfg := testConfig(t)
	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	cfg.RPC.Listen = ts.Listener.Addr().String()
	client := New(cfg)

	_, err = client.GetGraph(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected GetGraph to fail for missing graph")
	}

	apiErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *client.Error", err)
	}
	if apiErr.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", apiErr.StatusCode)
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "mock"
	return cfg
}

func toVerifySteps(steps []httpapi.StepEvidence) []httpapi.VerifyStep {
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
