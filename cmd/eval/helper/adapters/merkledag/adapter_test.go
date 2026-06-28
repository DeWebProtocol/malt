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

func TestAdapterAppliesOnlyChangedMutationBlobs(t *testing.T) {
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

	_, err = adapter.Apply(ctx, replay.CommitMutation{
		Repo:     "repo",
		Commit:   "c1",
		Snapshot: fakeSnapshot{"a-v1": []byte("a1"), "b-v1": []byte("b1")},
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationAdd, Path: "a.txt", Size: int64(len("a1")), Hash: "a-v1"},
			{Kind: replay.MutationAdd, Path: "b.txt", Size: int64(len("b1")), Hash: "b-v1"},
		},
		LiveFiles: []replay.LiveFile{
			{Path: "a.txt", Size: int64(len("a1")), Hash: "a-v1"},
			{Path: "b.txt", Size: int64(len("b1")), Hash: "b-v1"},
		},
	})
	if err != nil {
		t.Fatalf("Apply initial commit: %v", err)
	}

	result, err := adapter.Apply(ctx, replay.CommitMutation{
		Repo:     "repo",
		Commit:   "c2",
		Parent:   "c1",
		Snapshot: fakeSnapshot{"a-v2": []byte("a2")},
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationModify, Path: "a.txt", Size: int64(len("a2")), Hash: "a-v2"},
		},
		LiveFiles: []replay.LiveFile{
			{Path: "a.txt", Size: int64(len("a2")), Hash: "a-v2"},
			{Path: "b.txt", Size: int64(len("b1")), Hash: "b-v1"},
		},
	})
	if err != nil {
		t.Fatalf("Apply modify commit should not reread unchanged b.txt: %v", err)
	}
	if result.MaterializedPaths != 1 {
		t.Fatalf("materialized paths = %d, want only the changed path", result.MaterializedPaths)
	}
}

func TestAdapterAppliesRenameAndDeleteMutations(t *testing.T) {
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

	_, err = adapter.Apply(ctx, replay.CommitMutation{
		Repo:     "repo",
		Commit:   "c1",
		Snapshot: fakeSnapshot{"readme-v1": []byte("hello")},
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationAdd, Path: "README.md", Size: int64(len("hello")), Hash: "readme-v1"},
		},
	})
	if err != nil {
		t.Fatalf("Apply initial commit: %v", err)
	}
	result, err := adapter.Apply(ctx, replay.CommitMutation{
		Repo:     "repo",
		Commit:   "c2",
		Parent:   "c1",
		Snapshot: fakeSnapshot{"readme-v1": []byte("hello")},
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationRename, OldPath: "README.md", Path: "docs/README.md", Size: int64(len("hello")), Hash: "readme-v1"},
		},
	})
	if err != nil {
		t.Fatalf("Apply rename commit: %v", err)
	}
	if result.MaterializedPaths != 1 {
		t.Fatalf("rename materialized paths = %d, want 1", result.MaterializedPaths)
	}
	result, err = adapter.Apply(ctx, replay.CommitMutation{
		Repo:   "repo",
		Commit: "c3",
		Parent: "c2",
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationDelete, Path: "docs/README.md"},
		},
		Snapshot: fakeSnapshot{},
	})
	if err != nil {
		t.Fatalf("Apply delete commit: %v", err)
	}
	if result.MaterializedPaths != 0 {
		t.Fatalf("delete materialized paths = %d, want 0", result.MaterializedPaths)
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
