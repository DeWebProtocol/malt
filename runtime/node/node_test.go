package node

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/graph"
	runtimegraph "github.com/dewebprotocol/malt/runtime/graph"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func newTestCID(seed string) cid.Cid {
	mhash, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}

func TestCreateManagedGraphUsesNodeRuntimeProfile(t *testing.T) {
	node, err := NewNode(testNodeOptions(t)...)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer node.Close()

	meta, err := node.CreateManagedGraph(context.Background(), "managed", "")
	if err != nil {
		t.Fatalf("CreateManagedGraph failed: %v", err)
	}

	if meta.Backend != "kzg" {
		t.Fatalf("backend = %q, want %q", meta.Backend, "kzg")
	}
	if meta.ArcTableType != "versioned" {
		t.Fatalf("arctable_type = %q, want %q", meta.ArcTableType, "versioned")
	}
}

func TestOpenGraphUsesStoredBackend(t *testing.T) {
	node, err := NewNode(testNodeOptions(t)...)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer node.Close()

	if _, err := node.CreateManagedGraph(context.Background(), "kzg-graph", "kzg"); err != nil {
		t.Fatalf("CreateManagedGraph failed: %v", err)
	}

	g, err := node.OpenGraph(context.Background(), "kzg-graph")
	if err != nil {
		t.Fatalf("OpenGraph failed: %v", err)
	}

	root, err := g.Writer().CreateStructure(context.Background(), g.Namespace(), arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": newTestCID("payload"),
		"name":     newTestCID("alice"),
	}))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if got := maltcid.GetMaltCodec(root); got != maltcid.CodecMaltKZG {
		t.Fatalf("root codec = %x, want %x", got, maltcid.CodecMaltKZG)
	}
}

func TestOpenGraphUsesStoredIPABackend(t *testing.T) {
	cfg := testConfig(t)
	cfg.Structure.DefaultBackend = "ipa"

	node, err := NewNode(WithConfig(cfg), WithCAS(casmock.NewCAS()))
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer node.Close()

	if _, err := node.CreateManagedGraph(context.Background(), "ipa-graph", "ipa"); err != nil {
		t.Fatalf("CreateManagedGraph failed: %v", err)
	}

	g, err := node.OpenGraph(context.Background(), "ipa-graph")
	if err != nil {
		t.Fatalf("OpenGraph failed: %v", err)
	}

	root, err := g.Writer().CreateStructure(context.Background(), g.Namespace(), arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": newTestCID("payload"),
		"name":     newTestCID("alice"),
	}))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if got := maltcid.GetMaltCodec(root); got != maltcid.CodecMaltIPA {
		t.Fatalf("root codec = %x, want %x", got, maltcid.CodecMaltIPA)
	}
}

func TestNewGraphReturnsRuntimeContractWithNamespaceOption(t *testing.T) {
	node, err := NewNode(testNodeOptions(t)...)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer node.Close()

	g, err := node.NewGraph("default-id", runtimegraph.WithNamespace("custom-namespace"))
	if err != nil {
		t.Fatalf("NewGraph failed: %v", err)
	}
	var _ graph.Runtime = g
	if g.ID() != "default-id" {
		t.Fatalf("graph ID = %q, want default-id", g.ID())
	}
	if g.Namespace() != "custom-namespace" {
		t.Fatalf("graph namespace = %q, want custom-namespace", g.Namespace())
	}
	if g.Resolver() == nil || g.Writer() == nil {
		t.Fatalf("runtime graph must provide resolver and writer ports")
	}
}

func TestNewNodeWithFsKVStore(t *testing.T) {
	cfg := testConfig(t)
	cfg.State.KVStore.Type = "fs"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kvfs")

	node, err := NewNode(WithConfig(cfg), WithCAS(casmock.NewCAS()))
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer node.Close()

	if node.KVStore() == nil {
		t.Fatal("expected fs kvstore to be initialized")
	}
}

func TestOpenGraphRejectsArcTableMismatch(t *testing.T) {
	node, err := NewNode(testNodeOptions(t)...)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer node.Close()

	if _, err := node.GraphManager().CreateGraph(context.Background(), "legacy", "kzg", "overwrite"); err != nil {
		t.Fatalf("CreateGraph failed: %v", err)
	}

	_, err = node.OpenGraph(context.Background(), "legacy")
	if err == nil {
		t.Fatal("OpenGraph should reject ArcTable mismatch")
	}
	if !strings.Contains(err.Error(), `requires arctable_type "overwrite"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "badger"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = "http://127.0.0.1:4318"
	return cfg
}

func testNodeOptions(t *testing.T) []Option {
	t.Helper()
	return []Option{
		WithConfig(testConfig(t)),
		WithCAS(casmock.NewCAS()),
	}
}
