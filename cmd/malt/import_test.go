package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/server"
)

func TestBuildFileImportEntryDefaultsToBaseName(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	entry, err := buildFileImportEntry(file, "")
	if err != nil {
		t.Fatalf("build file entry: %v", err)
	}
	if entry.MaltPath != "hello.txt" {
		t.Fatalf("malt path = %q, want %q", entry.MaltPath, "hello.txt")
	}
}

func TestBuildDirectoryImportEntriesPreservesRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "readme.md"), []byte("readme"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "nested", "note.txt"), []byte("note"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	entries, err := buildDirectoryImportEntries(root, "repo")
	if err != nil {
		t.Fatalf("build directory entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[0].MaltPath != "repo/docs/nested/note.txt" {
		t.Fatalf("entries[0].MaltPath = %q", entries[0].MaltPath)
	}
	if entries[1].MaltPath != "repo/docs/readme.md" {
		t.Fatalf("entries[1].MaltPath = %q", entries[1].MaltPath)
	}
}

func TestImportWorkflowUploadsDirectoryAndCreatesGraph(t *testing.T) {
	ctx := context.Background()
	daemon := newImportTestDaemonClient(t)
	casClient := newImportTestCASClient(t)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("malt"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	entries, err := buildDirectoryImportEntries(root, "repo")
	if err != nil {
		t.Fatalf("build directory entries: %v", err)
	}

	arcs, totalBytes, err := uploadImportEntries(ctx, casClient, entries)
	if err != nil {
		t.Fatalf("upload entries: %v", err)
	}
	if len(arcs) != 2 {
		t.Fatalf("arc count = %d, want 2", len(arcs))
	}
	if totalBytes <= 0 {
		t.Fatalf("total bytes = %d, want > 0", totalBytes)
	}

	result, err := applyImportedArcs(ctx, daemon, importTarget{GraphID: "demo"}, arcs)
	if err != nil {
		t.Fatalf("apply imported arcs: %v", err)
	}
	if result.Graph != "demo" {
		t.Fatalf("result.Graph = %q, want %q", result.Graph, "demo")
	}
	if result.Root == "" {
		t.Fatal("expected non-empty graph root")
	}

	meta, err := daemon.GetGraph(ctx, "demo")
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	if meta.Root != result.Root {
		t.Fatalf("graph root = %q, want %q", meta.Root, result.Root)
	}
	if meta.ArcCount != 2 {
		t.Fatalf("graph arc_count = %d, want 2", meta.ArcCount)
	}

	resolveResp, err := daemon.ResolveGraph(ctx, "demo", "repo/src/main.go")
	if err != nil {
		t.Fatalf("resolve graph path: %v", err)
	}
	if resolveResp.Target != arcs["repo/src/main.go"] {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, arcs["repo/src/main.go"])
	}
}

func newImportTestDaemonClient(t *testing.T) *daemonclient.Client {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"
	cfg.CAS.Mode = "mock"

	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	t.Cleanup(ts.Close)
	return daemonclient.NewWithBaseURL(ts.URL + "/api/v1")
}

func newImportTestCASClient(t *testing.T) *ipfs.Client {
	t.Helper()

	mockCAS := casmock.NewCAS()
	mockHTTP := casmock.NewHTTPServer("127.0.0.1:0", mockCAS)
	ts := httptest.NewServer(mockHTTP.Handler())
	t.Cleanup(ts.Close)
	return ipfs.NewClient(ts.URL)
}
