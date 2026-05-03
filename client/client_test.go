package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/core/metrics"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	"github.com/dewebprotocol/malt/httpapi"
	"github.com/dewebprotocol/malt/server"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestClientRootFlow(t *testing.T) {
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
	current, err := client.GetCurrentRoot(ctx)
	if err != nil {
		t.Fatalf("get current root: %v", err)
	}
	if current.Root != "" {
		t.Fatalf("current root = %q, want empty for undefined root", current.Root)
	}
	loadedCurrent, err := client.GetCurrentRoot(ctx)
	if err != nil {
		t.Fatalf("get current root: %v", err)
	}
	if loadedCurrent.Root != "" {
		t.Fatalf("loaded current root = %q, want empty for undefined root", loadedCurrent.Root)
	}

	target := fakeCIDString("alice")
	createResp, err := client.CreateRootStructure(ctx, withPayloadBinding(map[string]string{"name": target}))
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

func TestClientCurrentStructureFlow(t *testing.T) {
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

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}

	target := fakeCIDString("managed-alice")
	createResp, err := client.CreateCurrentStructure(ctx, map[string]string{
		"@payload": fakeCIDString("managed-payload"),
		"name":     target,
		"/name":    target,
	})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}
	if createResp.Root == "" {
		t.Fatal("expected non-empty managed graph root")
	}

	resolveResp, err := client.ResolveCurrent(ctx, "name")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if resolveResp.Target != target {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, target)
	}

	meta, err := client.GetCurrentRoot(ctx)
	if err != nil {
		t.Fatalf("get root metadata: %v", err)
	}
	if meta.ArcCount != 2 {
		t.Fatalf("root arc_count = %d, want 2 after canonicalization and mandatory payload", meta.ArcCount)
	}

	updateTarget := fakeCIDString("managed-bob")
	updateResp, err := client.UpdateCurrent(ctx, "name", updateTarget)
	if err != nil {
		t.Fatalf("update root: %v", err)
	}
	if updateResp.NewRoot == createResp.Root {
		t.Fatal("expected root update to advance the head root")
	}

	snapshotResp, err := client.SnapshotCurrent(ctx)
	if err != nil {
		t.Fatalf("snapshot root: %v", err)
	}
	if snapshotResp.Arcs["name"] != updateTarget {
		t.Fatalf("snapshot target = %q, want %q", snapshotResp.Arcs["name"], updateTarget)
	}
	if len(snapshotResp.Arcs) != 2 {
		t.Fatalf("snapshot arc count = %d, want 2", len(snapshotResp.Arcs))
	}
}

func TestClientProofListReads(t *testing.T) {
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

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}

	target := fakeCIDString("client-prooflist-target")
	payload := fakeCIDString("client-prooflist-payload")
	_, err = client.CreateCurrentStructure(ctx, map[string]string{
		"@payload": payload,
		"name":     target,
	})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	rootProof, err := client.ProofListCurrent(ctx, "name")
	if err != nil {
		t.Fatalf("ProofListCurrent: %v", err)
	}
	if rootProof.Target != target {
		t.Fatalf("root prooflist target = %q, want %q", rootProof.Target, target)
	}
	if len(rootProof.ProofList.Steps) == 0 {
		t.Fatal("expected non-empty root prooflist")
	}

	rootPayload := fakeCIDString("client-root-prooflist-payload")
	rootCreateResp, err := client.CreateRootStructure(ctx, withPayloadBinding(map[string]string{
		"@payload": rootPayload,
	}))
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	rootProof, err = client.ProofListRoot(ctx, rootCreateResp.Root, "")
	if err != nil {
		t.Fatalf("ProofListRoot: %v", err)
	}
	if rootProof.Target != rootPayload {
		t.Fatalf("root prooflist target = %q, want %q", rootProof.Target, rootPayload)
	}
	if len(rootProof.ProofList.Steps) != 1 {
		t.Fatalf("root prooflist steps = %d, want 1", len(rootProof.ProofList.Steps))
	}
	if rootProof.ProofList.Steps[0].Kind != prooflist.KindPayloadBinding {
		t.Fatalf("root prooflist step kind = %q, want %q", rootProof.ProofList.Steps[0].Kind, prooflist.KindPayloadBinding)
	}
}

func TestClientMetricsSnapshotAndReset(t *testing.T) {
	seen := make(chan string, 2)
	snapshotResp := httpapi.MetricsResponse{
		Snapshot: metrics.Snapshot{
			CAS: metrics.CASStats{
				GetCount: 7,
				BytesGet: 11,
			},
			ArcTable: metrics.ArcTableStats{
				SnapshotCount: 2,
			},
			Proof: metrics.ProofStats{
				ProofListCount: 3,
				StepCount:      5,
				TotalBytes:     13,
			},
		},
	}
	resetResp := httpapi.MetricsResponse{}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/metrics":
			_ = json.NewEncoder(w).Encode(&snapshotResp)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/metrics:reset":
			_ = json.NewEncoder(w).Encode(&resetResp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	client := NewWithBaseURL(ts.URL + "/api/v1")
	snapshot, err := client.MetricsSnapshot(context.Background())
	if err != nil {
		t.Fatalf("MetricsSnapshot: %v", err)
	}
	if got := <-seen; got != "GET /api/v1/metrics" {
		t.Fatalf("MetricsSnapshot request = %q, want GET /api/v1/metrics", got)
	}
	if snapshot.Snapshot.CAS.GetCount != 7 || snapshot.Snapshot.ArcTable.SnapshotCount != 2 || snapshot.Snapshot.Proof.ProofListCount != 3 {
		t.Fatalf("decoded metrics snapshot = %+v", snapshot.Snapshot)
	}

	reset, err := client.ResetMetrics(context.Background())
	if err != nil {
		t.Fatalf("ResetMetrics: %v", err)
	}
	if got := <-seen; got != "POST /api/v1/metrics:reset" {
		t.Fatalf("ResetMetrics request = %q, want POST /api/v1/metrics:reset", got)
	}
	if reset.Snapshot != (metrics.Snapshot{}) {
		t.Fatalf("decoded reset snapshot = %+v, want zero counters", reset.Snapshot)
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

	_, err = client.ResolveCurrent(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected ResolveCurrent to fail for missing current root")
	}

	apiErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *client.Error", err)
	}
	if apiErr.StatusCode != 409 {
		t.Fatalf("status = %d, want 409", apiErr.StatusCode)
	}
}

func TestClientRootSet(t *testing.T) {
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

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}

	root1Resp, err := client.CreateCurrentMap(ctx, withPayloadBinding(map[string]string{
		"file.txt": fakeCIDString("head-file"),
	}))
	if err != nil {
		t.Fatalf("create map root: %v", err)
	}
	root1 := root1Resp.Root
	if err := client.SetCurrentRoot(ctx, root1, 2, ""); err != nil {
		t.Fatalf("set head: %v", err)
	}

	meta, err := client.GetCurrentRoot(ctx)
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	if meta.Root != root1 || meta.ArcCount != 2 {
		t.Fatalf("meta root=%q arcs=%d", meta.Root, meta.ArcCount)
	}

	// Stale expected old root should be rejected.
	if err := client.SetCurrentRoot(ctx, fakeCIDString("head-2"), 3, fakeCIDString("stale")); err == nil {
		t.Fatal("expected stale expected_old_root to fail")
	}
}

func TestClientScopedMapAndListAPIs(t *testing.T) {
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

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}

	target := fakeCIDString("bucket-map-target")
	mapCreate, err := client.CreateCurrentMap(ctx, withPayloadBinding(map[string]string{"docs/readme.md": target}))
	if err != nil {
		t.Fatalf("CreateCurrentMap: %v", err)
	}
	if mapCreate.Root == "" {
		t.Fatal("CreateCurrentMap returned empty root")
	}

	mapResolve, err := client.ResolveCurrentMap(ctx, mapCreate.Root, "docs/readme.md")
	if err != nil {
		t.Fatalf("ResolveCurrentMap: %v", err)
	}
	if mapResolve.Key != target {
		t.Fatalf("ResolveCurrentMap key = %q, want %q", mapResolve.Key, target)
	}

	if _, err := client.ResolveCurrentMap(ctx, mapCreate.Root, "missing"); err == nil {
		t.Fatal("expected ResolveCurrentMap to fail for missing path")
	}

	mapSnapshot, err := client.SnapshotCurrentMap(ctx, mapCreate.Root)
	if err != nil {
		t.Fatalf("SnapshotCurrentMap: %v", err)
	}
	if mapSnapshot.Bindings["docs/readme.md"] != target {
		t.Fatalf("SnapshotCurrentMap binding = %q, want %q", mapSnapshot.Bindings["docs/readme.md"], target)
	}

	chunk1 := fakeCIDString("chunk1")
	chunk2 := fakeCIDString("chunk2")
	listCreate, err := client.CreateCurrentList(ctx, []string{chunk1, chunk2}, 262144)
	if err != nil {
		t.Fatalf("CreateCurrentList: %v", err)
	}
	if listCreate.ChunkCount != 2 || listCreate.ChunkSize != 262144 {
		t.Fatalf("CreateCurrentList response = %+v", listCreate)
	}

	listStat, err := client.GetCurrentList(ctx, listCreate.Root)
	if err != nil {
		t.Fatalf("GetCurrentList: %v", err)
	}
	if listStat.ChunkCount != 2 || listStat.ChunkSize != 262144 {
		t.Fatalf("GetCurrentList response = %+v", listStat)
	}
}

func TestClientSemanticMutation(t *testing.T) {
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

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}
	createResp, err := client.CreateCurrentStructure(ctx, withPayloadBinding(map[string]string{
		"name": fakeCIDString("initial-name"),
	}))
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	nextName := fakeCIDString("next-name")
	resp, err := client.ApplyCurrentSemanticMutation(ctx, &httpapi.CurrentSemanticMutationRequest{
		Puts: []httpapi.SemanticMutationPut{{
			Object: createResp.Root,
			Kind:   "map",
			Entries: []httpapi.SemanticMutationEntry{
				{Path: "@payload", Target: fakeCIDString("next-payload")},
				{Path: "name", Target: nextName},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ApplyCurrentSemanticMutation: %v", err)
	}
	if resp.BaseRoot != createResp.Root || resp.NewRoot == "" {
		t.Fatalf("unexpected semantic mutation response: %+v", resp)
	}
	if resp.PutCount != 1 || resp.ArcCount != 2 {
		t.Fatalf("semantic mutation counts = puts %d arcs %d, want 1/2", resp.PutCount, resp.ArcCount)
	}

	meta, err := client.GetCurrentRoot(ctx)
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	if meta.Root != resp.NewRoot || meta.ArcCount != 2 {
		t.Fatalf("root=%q arcs=%d, want root=%q arcs=2", meta.Root, meta.ArcCount, resp.NewRoot)
	}

	resolved, err := client.ResolveCurrent(ctx, "name")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if resolved.Target != nextName {
		t.Fatalf("resolved target = %q, want %q", resolved.Target, nextName)
	}
}

func TestClientRootSemanticMutation(t *testing.T) {
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

	createResp, err := client.CreateRootStructure(ctx, withPayloadBinding(map[string]string{
		"name": fakeCIDString("initial-name"),
	}))
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	nextName := fakeCIDString("root-next-name")
	resp, err := client.ApplyRootSemanticMutation(ctx, createResp.Root, &httpapi.RootSemanticMutationRequest{
		Puts: []httpapi.SemanticMutationPut{{
			Object: createResp.Root,
			Kind:   "map",
			Entries: []httpapi.SemanticMutationEntry{
				{Path: "@payload", Target: fakeCIDString("root-next-payload")},
				{Path: "name", Target: nextName},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ApplyRootSemanticMutation: %v", err)
	}
	if resp.BaseRoot != createResp.Root || resp.NewRoot == "" || resp.NewRoot == createResp.Root {
		t.Fatalf("unexpected root semantic mutation response: %+v", resp)
	}
	if resp.PutCount != 1 || resp.ArcCount != 2 {
		t.Fatalf("semantic mutation counts = puts %d arcs %d, want 1/2", resp.PutCount, resp.ArcCount)
	}

	resolved, err := client.ResolveRoot(ctx, resp.NewRoot, "name")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if resolved.Target != nextName {
		t.Fatalf("resolved target = %q, want %q", resolved.Target, nextName)
	}
}

func TestClientStatAndContent(t *testing.T) {
	cfg := testConfig(t)
	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	cfg.RPC.Listen = ts.Listener.Addr().String()
	client := New(cfg)
	ctx := context.Background()

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}

	raw := []byte("abcdef")
	rawCID := cidFromBytes(raw)
	mockCAS.AddBlock(rawCID, raw)

	if _, err := client.CreateCurrentStructure(ctx, withPayloadBinding(map[string]string{"f.txt": rawCID.String()})); err != nil {
		t.Fatalf("create structure: %v", err)
	}

	stat, err := client.StatCurrentPath(ctx, "/f.txt")
	if err != nil {
		t.Fatalf("StatCurrentPath: %v", err)
	}
	if stat.Kind != "file" || stat.StorageKind != "raw" || stat.Size == nil || *stat.Size != int64(len(raw)) {
		t.Fatalf("unexpected stat: %+v", stat)
	}

	body, status, _, err := client.GetCurrentContent(ctx, "f.txt", "bytes=1-3")
	if err != nil {
		t.Fatalf("GetCurrentContent: %v", err)
	}
	if status != 206 || string(body) != "bcd" {
		t.Fatalf("unexpected status/body: %d %q", status, string(body))
	}
}

func TestClientContentProofReadReturnsContentRangeAndProofList(t *testing.T) {
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

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}
	if _, err := client.AddCurrentUnixFSFile(ctx, "f.txt", []byte("abcdef")); err != nil {
		t.Fatalf("add unixfs file: %v", err)
	}

	resp, err := client.GetCurrentContentProof(ctx, "f.txt", "bytes=1-3")
	if err != nil {
		t.Fatalf("GetCurrentContentProof: %v", err)
	}
	if string(resp.Content) != "bcd" {
		t.Fatalf("content = %q, want bcd", resp.Content)
	}
	if resp.Range.StatusCode != http.StatusPartialContent || resp.Range.ContentRange != "bytes 1-3/6" {
		t.Fatalf("range metadata = %+v, want status 206 content-range bytes 1-3/6", resp.Range)
	}
	if resp.Range.Start != 1 || resp.Range.EndExclusive != 4 || resp.Range.ContentLength != 3 || resp.Range.TotalSize != 6 {
		t.Fatalf("unexpected byte range metadata: %+v", resp.Range)
	}
	if err := resp.ProofList.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("prooflist shape: %v", err)
	}
	last := resp.ProofList.Steps[len(resp.ProofList.Steps)-1]
	if last.Kind != prooflist.KindPayloadBinding || last.Path != "@payload" {
		t.Fatalf("last proof step = %q/%q, want payload binding @payload", last.Kind, last.Path)
	}
}

func TestClientRestartSafety(t *testing.T) {
	cfg, casClient := persistentTestConfig(t)
	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create first node: %v", err)
	}
	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	cfg.RPC.Listen = ts.Listener.Addr().String()
	client := New(cfg)
	ctx := context.Background()

	if _, err := client.GetCurrentRoot(ctx); err != nil {
		t.Fatalf("create root: %v", err)
	}

	chunk1 := bytes.Repeat([]byte{'x'}, 262144)
	chunk2 := []byte("tail")
	chunk1CID, err := casClient.Put(ctx, chunk1)
	if err != nil {
		t.Fatalf("put chunk1: %v", err)
	}
	chunk2CID, err := casClient.Put(ctx, chunk2)
	if err != nil {
		t.Fatalf("put chunk2: %v", err)
	}
	listResp, err := client.CreateCurrentList(ctx, []string{chunk1CID.String(), chunk2CID.String()}, 262144)
	if err != nil {
		t.Fatalf("create root list: %v", err)
	}

	noteData := []byte("note after restart")
	noteCID, err := casClient.Put(ctx, noteData)
	if err != nil {
		t.Fatalf("put note: %v", err)
	}
	dirManifestCID := mustPutManifest(t, ctx, casClient, []string{"note.txt"})
	rootManifestCID := mustPutManifest(t, ctx, casClient, []string{"dir", "large.bin"})

	dirResp, err := client.CreateCurrentMap(ctx, map[string]string{
		"@payload": dirManifestCID.String(),
		"note.txt": noteCID.String(),
	})
	if err != nil {
		t.Fatalf("create dir map: %v", err)
	}
	rootResp, err := client.CreateCurrentMap(ctx, map[string]string{
		"@payload":     rootManifestCID.String(),
		"dir":          dirResp.Root,
		"dir/note.txt": noteCID.String(),
		"large.bin":    listResp.Root,
	})
	if err != nil {
		t.Fatalf("create root map: %v", err)
	}
	if err := client.SetCurrentRoot(ctx, rootResp.Root, 4, ""); err != nil {
		t.Fatalf("set root: %v", err)
	}

	ts.Close()
	if err := node.Close(); err != nil {
		t.Fatalf("close first node: %v", err)
	}

	restartedNode, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create restarted node: %v", err)
	}
	t.Cleanup(func() {
		_ = restartedNode.Close()
	})
	restartedTS := httptest.NewServer(server.New(restartedNode, "127.0.0.1:0").Handler())
	t.Cleanup(restartedTS.Close)
	cfg.RPC.Listen = restartedTS.Listener.Addr().String()
	restartedClient := New(cfg)

	meta, err := restartedClient.GetCurrentRoot(ctx)
	if err != nil {
		t.Fatalf("get root after restart: %v", err)
	}
	if meta.Root != rootResp.Root {
		t.Fatalf("root after restart = %q, want %q", meta.Root, rootResp.Root)
	}

	body, status, _, err := restartedClient.GetCurrentContent(ctx, "large.bin", "")
	if err != nil {
		t.Fatalf("read list-backed file after restart: status=%d err=%v", status, err)
	}
	if len(body) != len(chunk1)+len(chunk2) {
		t.Fatalf("list-backed content len = %d, want %d", len(body), len(chunk1)+len(chunk2))
	}
	if !bytes.Equal(body[len(body)-len(chunk2):], chunk2) {
		t.Fatal("list-backed file tail mismatch after restart")
	}

	dirStat, err := restartedClient.StatCurrentPath(ctx, "dir")
	if err != nil {
		t.Fatalf("stat dir after restart: %v", err)
	}
	if dirStat.Kind != "dir" || dirStat.Payload != dirManifestCID.String() {
		t.Fatalf("unexpected dir stat after restart: %+v", dirStat)
	}

	resolveResp, err := restartedClient.ResolveCurrent(ctx, "dir")
	if err != nil {
		t.Fatalf("resolve dir after restart: %v", err)
	}
	if resolveResp.Target != dirManifestCID.String() {
		t.Fatalf("resolved dir target = %q, want manifest %q", resolveResp.Target, dirManifestCID.String())
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

func persistentTestConfig(t *testing.T) (*config.Config, *ipfs.Client) {
	t.Helper()

	mockCAS := casmock.NewCAS()
	mockHTTP := casmock.NewHTTPServer("127.0.0.1:0", mockCAS)
	casTS := httptest.NewServer(mockHTTP.Handler())
	t.Cleanup(casTS.Close)

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "badger"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = casTS.URL
	return cfg, ipfs.NewClient(casTS.URL)
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

func cidFromBytes(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
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

func mustPutManifest(t *testing.T, ctx context.Context, casClient *ipfs.Client, entries []string) cid.Cid {
	t.Helper()
	payload, err := (&manifest.DirectoryManifest{Entries: entries}).MarshalJSON()
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	c, err := casClient.Put(ctx, payload)
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	return c
}
