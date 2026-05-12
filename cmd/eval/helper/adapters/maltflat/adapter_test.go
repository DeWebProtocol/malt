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
	if result.Accounting.Categories[evalstore.CategoryArcTable].NewObjectCount == 0 {
		t.Fatal("expected MALT-flat to charge ArcTable records")
	}
	rootHead := result.Accounting.Categories[evalstore.CategoryRootHead]
	if rootHead.NewPersistedBytes != uint64(len(result.Root)) {
		t.Fatalf("root/head bytes = %d, want emitted root string length %d", rootHead.NewPersistedBytes, len(result.Root))
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
