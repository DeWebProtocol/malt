package metrics

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/runtime/arctable/overwrite"
	"github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func newTestCID(data []byte) cid.Cid {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}

func TestArcTableStatsWrapperCountsCallsAndPreservesBehavior(t *testing.T) {
	base, err := overwrite.NewArcTable(overwrite.WithKVStore(memory.New()))
	if err != nil {
		t.Fatalf("new overwrite arctable: %v", err)
	}
	wrapped := NewArcTable(base)

	ctx := context.Background()
	namespace := "metrics-namespace"
	root := newTestCID([]byte("root"))
	targetA := newTestCID([]byte("target-a"))
	targetB := newTestCID([]byte("target-b"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{
		"a": targetA,
		"b": targetB,
	})

	if err := wrapped.Update(ctx, namespace, root, cid.Undef, arcs); err != nil {
		t.Fatalf("update arcs: %v", err)
	}
	got, err := wrapped.Get(ctx, namespace, root, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("get arc: %v", err)
	}
	if !got.Equals(targetA) {
		t.Fatalf("Get returned %s, want %s", got, targetA)
	}

	batch, err := wrapped.BatchGet(ctx, namespace, root, []arcset.Path{
		arcset.CanonicalizePath("a"),
		arcset.CanonicalizePath("b"),
	})
	if err != nil {
		t.Fatalf("batch get arcs: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("BatchGet returned %d arcs, want 2", len(batch))
	}

	snapshot, err := wrapped.Snapshot(ctx, namespace, root)
	if err != nil {
		t.Fatalf("snapshot arcs: %v", err)
	}
	if snapshot.Len() != 2 {
		t.Fatalf("Snapshot returned %d arcs, want 2", snapshot.Len())
	}

	iter := wrapped.Iterate(ctx, namespace, root)
	defer iter.Close()
	iterated := 0
	for {
		_, _, ok := iter.Next()
		if !ok {
			break
		}
		iterated++
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("iterate arcs: %v", err)
	}
	if iterated != 2 {
		t.Fatalf("Iterate returned %d arcs, want 2", iterated)
	}

	stats := wrapped.SnapshotStats()
	if stats.GetCount != 1 {
		t.Fatalf("GetCount = %d, want 1", stats.GetCount)
	}
	if stats.BatchGetCount != 1 {
		t.Fatalf("BatchGetCount = %d, want 1", stats.BatchGetCount)
	}
	if stats.BatchGetPathCount != 2 {
		t.Fatalf("BatchGetPathCount = %d, want 2", stats.BatchGetPathCount)
	}
	if stats.UpdateCount != 1 {
		t.Fatalf("UpdateCount = %d, want 1", stats.UpdateCount)
	}
	if stats.UpdateArcCount != 2 {
		t.Fatalf("UpdateArcCount = %d, want 2", stats.UpdateArcCount)
	}
	if stats.SnapshotCount != 1 {
		t.Fatalf("SnapshotCount = %d, want 1", stats.SnapshotCount)
	}
	if stats.SnapshotArcCount != 2 {
		t.Fatalf("SnapshotArcCount = %d, want 2", stats.SnapshotArcCount)
	}
	if stats.IterateCount != 1 {
		t.Fatalf("IterateCount = %d, want 1", stats.IterateCount)
	}
}

func TestArcTableStatsResetClearsCounters(t *testing.T) {
	base, err := overwrite.NewArcTable(overwrite.WithKVStore(memory.New()))
	if err != nil {
		t.Fatalf("new overwrite arctable: %v", err)
	}
	wrapped := NewArcTable(base)

	ctx := context.Background()
	_, _ = wrapped.Get(ctx, "namespace", cid.Undef, arcset.CanonicalizePath("missing"))

	wrapped.ResetStats()

	stats := wrapped.SnapshotStats()
	if stats != (ArcTableStats{}) {
		t.Fatalf("stats after reset = %+v, want zero", stats)
	}
}
