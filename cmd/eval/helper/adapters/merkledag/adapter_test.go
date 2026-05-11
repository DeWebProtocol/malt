package merkledag_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/merkledag"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/internal/merkledagimport"
)

func TestAdapterImportsOnlyTraceLiveFilesAndIgnoresGitDirectory(t *testing.T) {
	ctx := context.Background()
	snapshotRoot := t.TempDir()
	writeFile(t, snapshotRoot, "keep.txt", "keep")
	writeFile(t, snapshotRoot, ".git/config", "not part of snapshot")
	writeFile(t, snapshotRoot, "ignored.txt", "not listed in trace")

	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })
	system, err := factory.NewSystem(ctx, "merkledag")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	adapter := merkledag.New(system, merkledag.Options{
		Name:      "merkledag",
		DirLayout: merkledagimport.DirLayoutBasic,
	})

	result, err := adapter.Apply(ctx, replay.CommitMutation{
		Repo:         "repo",
		Commit:       "c1",
		SnapshotRoot: snapshotRoot,
		LiveFiles: []replay.LiveFile{
			{Path: "keep.txt", Size: int64(len("keep"))},
		},
		Mutations: []replay.FileMutation{{Kind: replay.MutationAdd, Path: "keep.txt"}},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Root == "" {
		t.Fatal("root is empty")
	}
	if result.MaterializedPaths != 1 {
		t.Fatalf("materialized paths = %d, want 1", result.MaterializedPaths)
	}
	if result.Accounting.Categories[evalstore.CategoryCASMetadata].NewObjectCount == 0 {
		t.Fatal("expected Merkle DAG metadata blocks")
	}
	if result.Accounting.Categories[evalstore.CategoryRootHead].NewPersistedBytes == 0 {
		t.Fatal("expected root/head metadata bytes")
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
