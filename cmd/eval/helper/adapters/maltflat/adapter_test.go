package maltflat_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/maltflat"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
)

func TestAdapterMaterializesLiveSnapshotWithIndependentStoreAccounting(t *testing.T) {
	ctx := context.Background()

	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })
	system, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	adapter, err := maltflat.New(system, maltflat.Options{Namespace: "test-maltflat", ChunkSize: 4})
	if err != nil {
		t.Fatalf("New adapter: %v", err)
	}

	result, err := adapter.Apply(ctx, replay.CommitMutation{
		Repo:     "repo",
		Commit:   "c1",
		Snapshot: fakeSnapshot{"blob-1": []byte("hello malt")},
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationAdd, Path: "docs/readme.txt", Size: int64(len("hello malt")), Hash: "blob-1"},
		},
		LiveFiles: []replay.LiveFile{
			{Path: "docs/readme.txt", Size: int64(len("hello malt")), Hash: "blob-1"},
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Root == "" {
		t.Fatal("root is empty")
	}
	if result.AppliedMutations != 1 || result.MaterializedPaths != 1 {
		t.Fatalf("applied/materialized = %d/%d, want 1/1", result.AppliedMutations, result.MaterializedPaths)
	}
	if result.Accounting.Categories[evalstore.CategoryCanonicalDelta].NewObjectCount == 0 {
		t.Fatal("expected MALT-flat to charge canonical delta records")
	}
	if result.Accounting.Categories[evalstore.CategoryArcTable].NewObjectCount != 0 {
		t.Fatalf("ArcTable records should be derived cache only: %+v", result.Accounting.Categories[evalstore.CategoryArcTable])
	}
	rootHead := result.Accounting.Categories[evalstore.CategoryRootHead]
	if rootHead.NewPersistedBytes != uint64(len(result.Root)) {
		t.Fatalf("root/head bytes = %d, want emitted root string length %d", rootHead.NewPersistedBytes, len(result.Root))
	}
	if result.MaterializationStrategy != maltflat.MaterializationStrategyIncrementalDelta {
		t.Fatalf("materialization strategy = %q, want %q", result.MaterializationStrategy, maltflat.MaterializationStrategyIncrementalDelta)
	}
	if result.AccountingDelta.Categories[evalstore.CategoryCanonicalDelta].NewObjectCount == 0 {
		t.Fatal("expected MALT-flat to report per-commit canonical delta")
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
	system, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	adapter, err := maltflat.New(system, maltflat.Options{Namespace: "test-maltflat-delta", ChunkSize: 4})
	if err != nil {
		t.Fatalf("New adapter: %v", err)
	}

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

func TestAdapterUpdatesFlatPathBindingForModifiedFile(t *testing.T) {
	ctx := context.Background()

	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })
	system, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	adapter, err := maltflat.New(system, maltflat.Options{Namespace: "test-maltflat-flat-path", ChunkSize: 4})
	if err != nil {
		t.Fatalf("New adapter: %v", err)
	}

	_, err = adapter.Apply(ctx, replay.CommitMutation{
		Repo:     "repo",
		Commit:   "c1",
		Snapshot: fakeSnapshot{"a-v1": []byte("a1")},
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationAdd, Path: "docs/a.txt", Size: int64(len("a1")), Hash: "a-v1"},
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
			{Kind: replay.MutationModify, Path: "docs/a.txt", Size: int64(len("a2")), Hash: "a-v2"},
		},
	})
	if err != nil {
		t.Fatalf("Apply modify commit: %v", err)
	}

	canonicalDelta := result.AccountingDelta.Categories[evalstore.CategoryCanonicalDelta]
	if canonicalDelta.ChangedRecordCount != 2 {
		t.Fatalf("changed canonical delta records = %d, want parent root plus one flat path binding update", canonicalDelta.ChangedRecordCount)
	}
	arctable := result.AccountingDelta.Categories[evalstore.CategoryArcTable]
	if arctable.ChangedRecordCount != 0 {
		t.Fatalf("ArcTable changed records = %d, want derived cache excluded from canonical writes", arctable.ChangedRecordCount)
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
	system, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}
	adapter, err := maltflat.New(system, maltflat.Options{Namespace: "test-maltflat-rename-delete", ChunkSize: 4})
	if err != nil {
		t.Fatalf("New adapter: %v", err)
	}

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

func TestDefaultOptionsUsePaperConfiguration(t *testing.T) {
	opts := maltflat.DefaultOptions()
	if opts.ArcTableMode != maltflat.ArcTableModeVersioned {
		t.Fatalf("default ArcTable mode = %q, want %q", opts.ArcTableMode, maltflat.ArcTableModeVersioned)
	}
	if opts.CommitmentBackend != maltflat.CommitmentBackendKZG {
		t.Fatalf("default commitment backend = %q, want %q", opts.CommitmentBackend, maltflat.CommitmentBackendKZG)
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
