package merkledag_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/merkledag"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/cmd/internal/merkledagimport"
)

func TestAdapterImportsOnlyTraceLiveFilesFromSnapshot(t *testing.T) {
	ctx := context.Background()

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
		Repo:   "repo",
		Commit: "c1",
		Snapshot: fakeSnapshot{
			"keep-blob":    []byte("keep"),
			"ignored-blob": []byte("not listed in trace"),
		},
		LiveFiles: []replay.LiveFile{
			{Path: "keep.txt", Size: int64(len("keep")), Hash: "keep-blob"},
		},
		Mutations: []replay.FileMutation{{Kind: replay.MutationAdd, Path: "keep.txt", Hash: "keep-blob"}},
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

type fakeSnapshot map[string][]byte

func (s fakeSnapshot) ReadBlob(_ context.Context, hash string) ([]byte, error) {
	data, ok := s[hash]
	if !ok {
		return nil, errMissingBlob(hash)
	}
	return data, nil
}

type errMissingBlob string

func (e errMissingBlob) Error() string {
	return "missing blob " + string(e)
}
