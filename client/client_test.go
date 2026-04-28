package client

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/httpapi"
	"github.com/dewebprotocol/malt/server"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestClientBucketFlow(t *testing.T) {
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
	bucket, err := client.CreateBucket(ctx, "demo", "")
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if bucket.ID != "demo" {
		t.Fatalf("bucket id = %q, want %q", bucket.ID, "demo")
	}
	if bucket.Root != "" {
		t.Fatalf("bucket root = %q, want empty for undefined head", bucket.Root)
	}
	loadedBucket, err := client.GetBucket(ctx, "demo")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if loadedBucket.ID != "demo" {
		t.Fatalf("loaded bucket id = %q, want %q", loadedBucket.ID, "demo")
	}
	if loadedBucket.Root != "" {
		t.Fatalf("loaded bucket root = %q, want empty for undefined head", loadedBucket.Root)
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

func TestClientManagedBucketStructureFlow(t *testing.T) {
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

	if _, err := client.CreateBucket(ctx, "demo", ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	target := fakeCIDString("managed-alice")
	createResp, err := client.CreateBucketStructure(ctx, "demo", map[string]string{
		"@payload": fakeCIDString("managed-payload"),
		"name":     target,
		"/name":    target,
	})
	if err != nil {
		t.Fatalf("create managed bucket structure: %v", err)
	}
	if createResp.Root == "" {
		t.Fatal("expected non-empty managed graph root")
	}

	resolveResp, err := client.ResolveBucket(ctx, "demo", "name")
	if err != nil {
		t.Fatalf("resolve managed bucket: %v", err)
	}
	if resolveResp.Target != target {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, target)
	}

	meta, err := client.GetBucket(ctx, "demo")
	if err != nil {
		t.Fatalf("get managed bucket metadata: %v", err)
	}
	if meta.ArcCount != 2 {
		t.Fatalf("managed bucket arc_count = %d, want 2 after canonicalization and mandatory payload", meta.ArcCount)
	}

	updateTarget := fakeCIDString("managed-bob")
	updateResp, err := client.UpdateBucket(ctx, "demo", "name", updateTarget)
	if err != nil {
		t.Fatalf("update managed bucket: %v", err)
	}
	if updateResp.NewRoot == createResp.Root {
		t.Fatal("expected managed bucket update to advance the head root")
	}

	snapshotResp, err := client.SnapshotBucket(ctx, "demo")
	if err != nil {
		t.Fatalf("snapshot managed bucket: %v", err)
	}
	if snapshotResp.Arcs["name"] != updateTarget {
		t.Fatalf("snapshot target = %q, want %q", snapshotResp.Arcs["name"], updateTarget)
	}
	if len(snapshotResp.Arcs) != 2 {
		t.Fatalf("snapshot arc count = %d, want 2", len(snapshotResp.Arcs))
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

	_, err = client.GetBucket(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected GetBucket to fail for missing bucket")
	}

	apiErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("error type = %T, want *client.Error", err)
	}
	if apiErr.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", apiErr.StatusCode)
	}
}

func TestClientBucketHeadSet(t *testing.T) {
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

	if _, err := client.CreateBucket(ctx, "demo", ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	root1Resp, err := client.CreateBucketMap(ctx, "demo", withPayloadBinding(map[string]string{
		"file.txt": fakeCIDString("head-file"),
	}))
	if err != nil {
		t.Fatalf("create map root: %v", err)
	}
	root1 := root1Resp.Root
	if err := client.SetBucketHead(ctx, "demo", root1, 2, ""); err != nil {
		t.Fatalf("set head: %v", err)
	}

	meta, err := client.GetBucket(ctx, "demo")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if meta.Root != root1 || meta.ArcCount != 2 {
		t.Fatalf("meta root=%q arcs=%d", meta.Root, meta.ArcCount)
	}

	// Stale expected old root should be rejected.
	if err := client.SetBucketHead(ctx, "demo", fakeCIDString("head-2"), 3, fakeCIDString("stale")); err == nil {
		t.Fatal("expected stale expected_old_root to fail")
	}
}

func TestClientBucketScopedMapAndListAPIs(t *testing.T) {
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

	if _, err := client.CreateBucket(ctx, "demo", ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	target := fakeCIDString("bucket-map-target")
	mapCreate, err := client.CreateBucketMap(ctx, "demo", withPayloadBinding(map[string]string{"docs/readme.md": target}))
	if err != nil {
		t.Fatalf("CreateBucketMap: %v", err)
	}
	if mapCreate.Root == "" {
		t.Fatal("CreateBucketMap returned empty root")
	}

	mapResolve, err := client.ResolveBucketMap(ctx, "demo", mapCreate.Root, "docs/readme.md")
	if err != nil {
		t.Fatalf("ResolveBucketMap: %v", err)
	}
	if mapResolve.Key != target {
		t.Fatalf("ResolveBucketMap key = %q, want %q", mapResolve.Key, target)
	}

	if _, err := client.ResolveBucketMap(ctx, "demo", mapCreate.Root, "missing"); err == nil {
		t.Fatal("expected ResolveBucketMap to fail for missing path")
	}

	mapSnapshot, err := client.SnapshotBucketMap(ctx, "demo", mapCreate.Root)
	if err != nil {
		t.Fatalf("SnapshotBucketMap: %v", err)
	}
	if mapSnapshot.Bindings["docs/readme.md"] != target {
		t.Fatalf("SnapshotBucketMap binding = %q, want %q", mapSnapshot.Bindings["docs/readme.md"], target)
	}

	chunk1 := fakeCIDString("chunk1")
	chunk2 := fakeCIDString("chunk2")
	listCreate, err := client.CreateBucketList(ctx, "demo", []string{chunk1, chunk2}, 262144)
	if err != nil {
		t.Fatalf("CreateBucketList: %v", err)
	}
	if listCreate.ChunkCount != 2 || listCreate.ChunkSize != 262144 {
		t.Fatalf("CreateBucketList response = %+v", listCreate)
	}

	listStat, err := client.GetBucketList(ctx, "demo", listCreate.Root)
	if err != nil {
		t.Fatalf("GetBucketList: %v", err)
	}
	if listStat.ChunkCount != 2 || listStat.ChunkSize != 262144 {
		t.Fatalf("GetBucketList response = %+v", listStat)
	}
}

func TestClientBucketSemanticMutation(t *testing.T) {
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

	if _, err := client.CreateBucket(ctx, "demo", ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	createResp, err := client.CreateBucketStructure(ctx, "demo", withPayloadBinding(map[string]string{
		"name": fakeCIDString("initial-name"),
	}))
	if err != nil {
		t.Fatalf("create bucket structure: %v", err)
	}

	nextName := fakeCIDString("next-name")
	resp, err := client.ApplyBucketSemanticMutation(ctx, "demo", &httpapi.BucketSemanticMutationRequest{
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
		t.Fatalf("ApplyBucketSemanticMutation: %v", err)
	}
	if resp.Bucket != "demo" || resp.BaseRoot != createResp.Root || resp.NewRoot == "" {
		t.Fatalf("unexpected semantic mutation response: %+v", resp)
	}
	if resp.PutCount != 1 || resp.ArcCount != 2 {
		t.Fatalf("semantic mutation counts = puts %d arcs %d, want 1/2", resp.PutCount, resp.ArcCount)
	}

	meta, err := client.GetBucket(ctx, "demo")
	if err != nil {
		t.Fatalf("get bucket: %v", err)
	}
	if meta.Root != resp.NewRoot || meta.ArcCount != 2 {
		t.Fatalf("bucket root=%q arcs=%d, want root=%q arcs=2", meta.Root, meta.ArcCount, resp.NewRoot)
	}

	resolved, err := client.ResolveBucket(ctx, "demo", "name")
	if err != nil {
		t.Fatalf("resolve bucket: %v", err)
	}
	if resolved.Target != nextName {
		t.Fatalf("resolved target = %q, want %q", resolved.Target, nextName)
	}
}

func TestClientBucketStatAndContent(t *testing.T) {
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

	if _, err := client.CreateBucket(ctx, "demo", ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	raw := []byte("abcdef")
	rawCID := cidFromBytes(raw)
	mockCAS.AddBlock(rawCID, raw)

	if _, err := client.CreateBucketStructure(ctx, "demo", withPayloadBinding(map[string]string{"f.txt": rawCID.String()})); err != nil {
		t.Fatalf("create structure: %v", err)
	}

	stat, err := client.StatBucketPath(ctx, "demo", "/f.txt")
	if err != nil {
		t.Fatalf("StatBucketPath: %v", err)
	}
	if stat.Kind != "file" || stat.StorageKind != "raw" || stat.Size == nil || *stat.Size != int64(len(raw)) {
		t.Fatalf("unexpected stat: %+v", stat)
	}

	body, status, _, err := client.GetBucketContent(ctx, "demo", "f.txt", "bytes=1-3")
	if err != nil {
		t.Fatalf("GetBucketContent: %v", err)
	}
	if status != 206 || string(body) != "bcd" {
		t.Fatalf("unexpected status/body: %d %q", status, string(body))
	}
}

func TestClientBucketRestartSafety(t *testing.T) {
	cfg, casClient := persistentTestConfig(t)
	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create first node: %v", err)
	}
	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	cfg.RPC.Listen = ts.Listener.Addr().String()
	client := New(cfg)
	ctx := context.Background()

	if _, err := client.CreateBucket(ctx, "demo", ""); err != nil {
		t.Fatalf("create bucket: %v", err)
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
	listResp, err := client.CreateBucketList(ctx, "demo", []string{chunk1CID.String(), chunk2CID.String()}, 262144)
	if err != nil {
		t.Fatalf("create bucket list: %v", err)
	}

	noteData := []byte("note after restart")
	noteCID, err := casClient.Put(ctx, noteData)
	if err != nil {
		t.Fatalf("put note: %v", err)
	}
	dirManifestCID := mustPutManifest(t, ctx, casClient, []string{"note.txt"})
	rootManifestCID := mustPutManifest(t, ctx, casClient, []string{"dir", "large.bin"})

	dirResp, err := client.CreateBucketMap(ctx, "demo", map[string]string{
		"@payload": dirManifestCID.String(),
		"note.txt": noteCID.String(),
	})
	if err != nil {
		t.Fatalf("create dir map: %v", err)
	}
	rootResp, err := client.CreateBucketMap(ctx, "demo", map[string]string{
		"@payload":     rootManifestCID.String(),
		"dir":          dirResp.Root,
		"dir/note.txt": noteCID.String(),
		"large.bin":    listResp.Root,
	})
	if err != nil {
		t.Fatalf("create root map: %v", err)
	}
	if err := client.SetBucketHead(ctx, "demo", rootResp.Root, 4, ""); err != nil {
		t.Fatalf("set bucket head: %v", err)
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

	meta, err := restartedClient.GetBucket(ctx, "demo")
	if err != nil {
		t.Fatalf("get bucket after restart: %v", err)
	}
	if meta.Root != rootResp.Root {
		t.Fatalf("bucket root after restart = %q, want %q", meta.Root, rootResp.Root)
	}

	body, status, _, err := restartedClient.GetBucketContent(ctx, "demo", "large.bin", "")
	if err != nil {
		t.Fatalf("read list-backed file after restart: status=%d err=%v", status, err)
	}
	if len(body) != len(chunk1)+len(chunk2) {
		t.Fatalf("list-backed content len = %d, want %d", len(body), len(chunk1)+len(chunk2))
	}
	if !bytes.Equal(body[len(body)-len(chunk2):], chunk2) {
		t.Fatal("list-backed file tail mismatch after restart")
	}

	dirStat, err := restartedClient.StatBucketPath(ctx, "demo", "dir")
	if err != nil {
		t.Fatalf("stat dir after restart: %v", err)
	}
	if dirStat.Kind != "dir" || dirStat.Payload != dirManifestCID.String() {
		t.Fatalf("unexpected dir stat after restart: %+v", dirStat)
	}

	resolveResp, err := restartedClient.ResolveBucket(ctx, "demo", "dir")
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
