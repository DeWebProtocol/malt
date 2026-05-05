package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

	resolved, err := client.Resolve(ctx, updateResp.NewRoot, "name")
	if err != nil {
		t.Fatalf("resolve updated root: %v", err)
	}
	if resolved.Target != updateTarget {
		t.Fatalf("resolved target = %q, want %q", resolved.Target, updateTarget)
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

	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}
	targetData := []byte("client-prooflist-target")
	targetCID, err := mockCAS.Put(context.Background(), targetData)
	if err != nil {
		t.Fatalf("put target: %v", err)
	}
	payloadData := []byte("client-prooflist-payload")
	payloadCID, err := mockCAS.Put(context.Background(), payloadData)
	if err != nil {
		t.Fatalf("put payload: %v", err)
	}

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	cfg.RPC.Listen = ts.Listener.Addr().String()
	client := New(cfg)
	ctx := context.Background()

	target := targetCID.String()
	payload := payloadCID.String()
	createResp, err := client.CreateRootStructure(ctx, map[string]string{
		"@payload": payload,
		"name":     target,
	})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	rootProof, err := client.ProofList(ctx, createResp.Root, "name")
	if err != nil {
		t.Fatalf("ProofList: %v", err)
	}
	if rootProof.Target != target {
		t.Fatalf("root prooflist target = %q, want %q", rootProof.Target, target)
	}
	if len(rootProof.ProofList.Steps) == 0 {
		t.Fatal("expected non-empty root prooflist")
	}

	rootPayloadData := []byte("client-root-prooflist-payload")
	rootPayloadCID, err := mockCAS.Put(context.Background(), rootPayloadData)
	if err != nil {
		t.Fatalf("put root payload: %v", err)
	}
	rootPayload := rootPayloadCID.String()
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

func TestClientProofListRootPreservesPath(t *testing.T) {
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

	client := NewWithBaseURL(ts.URL)
	ctx := context.Background()

	targetData := []byte("named-target")
	targetCID, err := mockCAS.Put(ctx, targetData)
	if err != nil {
		t.Fatalf("put target: %v", err)
	}
	rootPayloadData := []byte("root-payload")
	rootPayloadCID, err := mockCAS.Put(ctx, rootPayloadData)
	if err != nil {
		t.Fatalf("put root payload: %v", err)
	}

	createResp, err := client.CreateRootStructure(ctx, withPayloadBinding(map[string]string{
		"@payload": rootPayloadCID.String(),
		"name":     targetCID.String(),
	}))
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	proof, err := client.ProofListRoot(ctx, createResp.Root, "name")
	if err != nil {
		t.Fatalf("ProofListRoot: %v", err)
	}
	if proof.Target != targetCID.String() {
		t.Fatalf("ProofListRoot target = %q, want %q", proof.Target, targetCID.String())
	}
}

func TestClientProveRootReturnsTranscript(t *testing.T) {
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

	client := NewWithBaseURL(ts.URL)
	ctx := context.Background()

	targetData := []byte("prove-target")
	targetCID, err := mockCAS.Put(ctx, targetData)
	if err != nil {
		t.Fatalf("put target: %v", err)
	}

	createResp, err := client.CreateRootStructure(ctx, withPayloadBinding(map[string]string{
		"name": targetCID.String(),
	}))
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	proof, err := client.ProveRoot(ctx, createResp.Root, "name")
	if err != nil {
		t.Fatalf("ProveRoot: %v", err)
	}
	if proof.Target != targetCID.String() {
		t.Fatalf("target = %q, want %q", proof.Target, targetCID.String())
	}
	if len(proof.Transcript) == 0 {
		t.Fatalf("transcript is empty")
	}
	verifyResp, err := client.Verify(ctx, &httpapi.VerifyRequest{
		Root:       createResp.Root,
		Transcript: toVerifySteps(proof.Transcript),
	})
	if err != nil {
		t.Fatalf("Verify ProveRoot transcript: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatalf("ProveRoot transcript did not verify")
	}
}

func TestClientProveRootDoesNotRequireTargetContent(t *testing.T) {
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

	client := NewWithBaseURL(ts.URL)
	ctx := context.Background()

	target := fakeCIDString("missing-content-target")
	createResp, err := client.CreateRootStructure(ctx, withPayloadBinding(map[string]string{
		"name": target,
	}))
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	proof, err := client.ProveRoot(ctx, createResp.Root, "name")
	if err != nil {
		t.Fatalf("ProveRoot: %v", err)
	}
	if proof.Target != target {
		t.Fatalf("target = %q, want %q", proof.Target, target)
	}
	if len(proof.Transcript) == 0 {
		t.Fatalf("transcript is empty")
	}
}

func TestClientRootPathMethodsPreserveNestedPath(t *testing.T) {
	seen := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.URL.RequestURI()
		w.Header().Set("X-Malt-Key", fakeCIDString("target"))
		w.Header().Set("X-Malt-Kind", "file")
		w.Header().Set("X-Malt-Storage-Kind", "raw")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewWithBaseURL(ts.URL)
	root := fakeCIDString("root")

	if _, err := client.Resolve(context.Background(), root, "a/b/c"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := <-seen; !strings.Contains(got, "/"+root+"/a/b/c") {
		t.Fatalf("request = %q, want nested root path", got)
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
		case r.Method == http.MethodGet && r.URL.Path == "/metrics":
			_ = json.NewEncoder(w).Encode(&snapshotResp)
		case r.Method == http.MethodPost && r.URL.Path == "/metrics:reset":
			_ = json.NewEncoder(w).Encode(&resetResp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	client := NewWithBaseURL(ts.URL)
	snapshot, err := client.MetricsSnapshot(context.Background())
	if err != nil {
		t.Fatalf("MetricsSnapshot: %v", err)
	}
	if got := <-seen; got != "GET /metrics" {
		t.Fatalf("MetricsSnapshot request = %q, want GET /metrics", got)
	}
	if snapshot.Snapshot.CAS.GetCount != 7 || snapshot.Snapshot.ArcTable.SnapshotCount != 2 || snapshot.Snapshot.Proof.ProofListCount != 3 {
		t.Fatalf("decoded metrics snapshot = %+v", snapshot.Snapshot)
	}

	reset, err := client.ResetMetrics(context.Background())
	if err != nil {
		t.Fatalf("ResetMetrics: %v", err)
	}
	if got := <-seen; got != "POST /metrics:reset" {
		t.Fatalf("ResetMetrics request = %q, want POST /metrics:reset", got)
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

	_, err = client.Resolve(context.Background(), "bafyroot", "missing")
	if err == nil {
		t.Fatal("expected Resolve to fail for missing root")
	}

	apiErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *client.Error", err)
	}
	if apiErr.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", apiErr.StatusCode)
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
	resp, err := client.ApplyRootSemanticMutation(ctx, createResp.Root, &httpapi.SemanticMutationRequest{
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

	raw := []byte("abcdef")
	rawCID := cidFromBytes(raw)
	mockCAS.AddBlock(rawCID, raw)

	createResp, err := client.CreateRootStructure(ctx, withPayloadBinding(map[string]string{"f.txt": rawCID.String()}))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}

	stat, err := client.Stat(ctx, createResp.Root, "f.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Kind != "file" || stat.StorageKind != "raw" || stat.Size == nil || *stat.Size != int64(len(raw)) {
		t.Fatalf("unexpected stat: %+v", stat)
	}

	body, status, _, err := client.GetContent(ctx, createResp.Root, "f.txt", "bytes=1-3")
	if err != nil {
		t.Fatalf("GetContent: %v", err)
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

	createResp, err := client.CreatePayloadRoot(ctx, nil)
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}
	writeResp, err := client.AddUnixFSFile(ctx, createResp.Root, "f.txt", []byte("abcdef"))
	if err != nil {
		t.Fatalf("add unixfs file: %v", err)
	}

	resp, err := client.ContentProof(ctx, writeResp.NewRoot, "f.txt", "bytes=1-3")
	if err != nil {
		t.Fatalf("ContentProof: %v", err)
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

	createResp, err := client.CreatePayloadRoot(ctx, nil)
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}
	root := createResp.Root

	chunk1 := bytes.Repeat([]byte{'x'}, 262144)
	chunk2 := []byte("tail")
	chunk1CID, err := casClient.Put(ctx, chunk1)
	if err != nil {
		t.Fatalf("put chunk1: %v", err)
	}
	if _, err := casClient.Put(ctx, chunk2); err != nil {
		t.Fatalf("put chunk2: %v", err)
	}

	noteData := []byte("note after restart")
	noteCID, err := casClient.Put(ctx, noteData)
	if err != nil {
		t.Fatalf("put note: %v", err)
	}
	dirManifestCID := mustPutManifest(t, ctx, casClient, []string{"note.txt"})
	rootManifestCID := mustPutManifest(t, ctx, casClient, []string{"dir", "large.bin"})

	// Create dir map via structure
	dirResp, err := client.CreateRootStructure(ctx, map[string]string{
		"@payload": dirManifestCID.String(),
		"note.txt": noteCID.String(),
	})
	if err != nil {
		t.Fatalf("create dir structure: %v", err)
	}

	// Create root map via structure
	rootResp, err := client.CreateRootStructure(ctx, map[string]string{
		"@payload":     rootManifestCID.String(),
		"dir":          dirResp.Root,
		"dir/note.txt": noteCID.String(),
		"large.bin":    chunk1CID.String(),
	})
	if err != nil {
		t.Fatalf("create root map structure: %v", err)
	}
	_ = rootResp // root CID for the new root map

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

	// Verify the original root structure is still resolvable
	resolveResp, err := restartedClient.Resolve(ctx, root, "@payload")
	if err != nil {
		t.Fatalf("resolve @payload after restart: %v", err)
	}
	if resolveResp.Target == "" {
		t.Fatal("expected resolved target after restart")
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
