package versioned

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/bloom"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/types/arcset"
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

// === Versioned ArcTable Tests ===

func TestVersionedArcTableNew(t *testing.T) {
	kv := memory.New()

	// Valid creation
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}
	if arctable == nil {
		t.Error("arctable should not be nil")
	}

	// Nil KVStore
	_, err = NewArcTable()
	if err == nil {
		t.Error("expected error for nil KVStore")
	}
}

func TestVersionedArcTableUpdate(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "versioned-graph"
	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create first version (no parent)
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = arctable.Update(ctx, bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get at root1
	got, err := arctable.Get(ctx, bucketId, root1, "a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, err = arctable.Get(ctx, bucketId, root1, "b")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}
}

func TestVersionedArcTableVersionChain(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "chain-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// Version 1: a -> target1, b -> target2
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = arctable.Update(ctx, bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update v1 failed: %v", err)
	}

	// Version 2: a -> target3 (override), b unchanged
	arcs2 := map[string]cid.Cid{
		"a": target3,
	}
	err = arctable.Update(ctx, bucketId, root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update v2 failed: %v", err)
	}

	// Version 3: c -> target3 (new), a and b unchanged
	arcs3 := map[string]cid.Cid{
		"c": target3,
	}
	err = arctable.Update(ctx, bucketId, root3, root2, arcs3)
	if err != nil {
		t.Fatalf("Update v3 failed: %v", err)
	}

	// Test resolution at root3

	// a should resolve to target3 (overridden at v2)
	got, err := arctable.Get(ctx, bucketId, root3, "a")
	if err != nil {
		t.Fatalf("Get a at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root3 should be target3")
	}

	// b should resolve to target2 (from v1)
	got, err = arctable.Get(ctx, bucketId, root3, "b")
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	// c should resolve to target3 (new at v3)
	got, err = arctable.Get(ctx, bucketId, root3, "c")
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("c at root3 should be target3")
	}

	// Test resolution at root2

	// a at root2 should be target3
	got, err = arctable.Get(ctx, bucketId, root2, "a")
	if err != nil {
		t.Fatalf("Get a at root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root2 should be target3")
	}

	// b at root2 should be target2
	got, err = arctable.Get(ctx, bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// c at root2 should not exist
	_, err = arctable.Get(ctx, bucketId, root2, "c")
	if err == nil {
		t.Error("c at root2 should not exist")
	}

	// Test resolution at root1

	got, err = arctable.Get(ctx, bucketId, root1, "a")
	if err != nil {
		t.Fatalf("Get a at root1 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("a at root1 should be target1")
	}
}

func TestVersionedArcTableGetParent(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "parent-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	arcs1 := map[string]cid.Cid{
		"a": newTestCID([]byte("target1")),
	}
	arctable.Update(ctx, bucketId, root1, cid.Undef, arcs1)

	arcs2 := map[string]cid.Cid{
		"b": newTestCID([]byte("target2")),
	}
	arctable.Update(ctx, bucketId, root2, root1, arcs2)

	// GetParent
	parent, err := arctable.GetParent(ctx, bucketId, root2)
	if err != nil {
		t.Fatalf("GetParent failed: %v", err)
	}
	if !parent.Equals(root1) {
		t.Error("parent of root2 should be root1")
	}

	// First version has no parent
	parent, err = arctable.GetParent(ctx, bucketId, root1)
	if err != nil {
		t.Fatalf("GetParent root1 failed: %v", err)
	}
	if parent != cid.Undef {
		t.Error("root1 should have no parent")
	}
}

func TestVersionedArcTableSnapshot(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "snapshot-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	arcs1 := map[string]cid.Cid{
		"a": target1,
	}
	arctable.Update(ctx, bucketId, root1, cid.Undef, arcs1)

	arcs2 := map[string]cid.Cid{
		"b": target2,
	}
	arctable.Update(ctx, bucketId, root2, root1, arcs2)

	// Snapshot at root2
	snapshot, err := arctable.Snapshot(ctx, bucketId, root2)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	got, ok := snapshot.Get(arcset.CanonicalizePath("a"))
	if !ok {
		t.Error("expected to find 'a' at root2 snapshot")
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, ok = snapshot.Get(arcset.CanonicalizePath("b"))
	if !ok {
		t.Error("expected to find 'b' at root2 snapshot")
	}
	if !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}

	// Len
	if snapshot.Len() != 2 {
		t.Errorf("expected Len 2, got %d", snapshot.Len())
	}
}

func TestVersionedArcTableBatchGet(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "batchget-graph"
	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// Setup arcs at root1
	arctable.Update(ctx, bucketId, root1, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	})

	// Test: all paths found
	results, err := arctable.BatchGet(ctx, bucketId, root1, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchGet failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	if !results["a"].Equals(target1) {
		t.Error("wrong value for 'a'")
	}
	if !results["b"].Equals(target2) {
		t.Error("wrong value for 'b'")
	}
	if !results["c"].Equals(target3) {
		t.Error("wrong value for 'c'")
	}

	// Test: some paths not found
	results, err = arctable.BatchGet(ctx, bucketId, root1, []string{"a", "notexist", "b"})
	if err != nil {
		t.Fatalf("BatchGet with missing paths failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (missing path omitted), got %d", len(results))
	}

	// Test: empty paths
	results, err = arctable.BatchGet(ctx, bucketId, root1, []string{})
	if err != nil {
		t.Fatalf("BatchGet with empty paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty paths, got %d", len(results))
	}

	// Test: all paths not found
	results, err = arctable.BatchGet(ctx, bucketId, root1, []string{"x", "y", "z"})
	if err != nil {
		t.Fatalf("BatchGet with all missing paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestVersionedArcTableBatchGetVersionChain(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "batchget-chain-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))
	target4 := newTestCID([]byte("target4"))

	// v1: a, b
	arctable.Update(ctx, bucketId, root1, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
	})

	// v2: c (new), a overridden
	arctable.Update(ctx, bucketId, root2, root1, map[string]cid.Cid{
		"a": target3,
		"c": target3,
	})

	// v3: d (new)
	arctable.Update(ctx, bucketId, root3, root2, map[string]cid.Cid{
		"d": target4,
	})

	// BatchGet at root3 should find all paths
	results, err := arctable.BatchGet(ctx, bucketId, root3, []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("BatchGet root3 failed: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}

	// a should be target3 (overridden at v2)
	if !results["a"].Equals(target3) {
		t.Error("'a' should be target3 (overridden)")
	}

	// b should be target2 (from v1)
	if !results["b"].Equals(target2) {
		t.Error("'b' should be target2 (from v1)")
	}

	// c should be target3 (from v2)
	if !results["c"].Equals(target3) {
		t.Error("'c' should be target3 (from v2)")
	}

	// d should be target4 (from v3)
	if !results["d"].Equals(target4) {
		t.Error("'d' should be target4 (from v3)")
	}

	// BatchGet at root2 should not find 'd'
	results, err = arctable.BatchGet(ctx, bucketId, root2, []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("BatchGet root2 failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results at root2, got %d", len(results))
	}
	if results["d"] != cid.Undef {
		t.Error("'d' should not be found at root2")
	}

	// BatchGet at root1 should find original 'a'
	results, err = arctable.BatchGet(ctx, bucketId, root1, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchGet root1 failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results at root1, got %d", len(results))
	}
	if !results["a"].Equals(target1) {
		t.Error("'a' at root1 should be target1 (original)")
	}
	if !results["b"].Equals(target2) {
		t.Error("'b' at root1 should be target2")
	}
	if results["c"] != cid.Undef {
		t.Error("'c' should not be found at root1")
	}
}

func TestVersionedArcTableBatchGetWithTombstone(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "batchget-tombstone-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// v1: a, b, c
	arctable.Update(ctx, bucketId, root1, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	})

	// v2: delete 'a' (tombstone), add 'd'
	arctable.Update(ctx, bucketId, root2, root1, map[string]cid.Cid{
		"a": cid.Undef, // tombstone
		"d": target3,
	})

	// v3: delete 'b' (tombstone)
	arctable.Update(ctx, bucketId, root3, root2, map[string]cid.Cid{
		"b": cid.Undef, // tombstone
	})

	// BatchGet at root3: a and b deleted, c and d exist
	results, err := arctable.BatchGet(ctx, bucketId, root3, []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("BatchGet root3 failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (a,b deleted), got %d", len(results))
	}
	if results["a"] != cid.Undef {
		t.Error("'a' should be deleted (tombstone)")
	}
	if results["b"] != cid.Undef {
		t.Error("'b' should be deleted (tombstone)")
	}
	if !results["c"].Equals(target3) {
		t.Error("'c' should still exist")
	}
	if !results["d"].Equals(target3) {
		t.Error("'d' should exist")
	}

	// BatchGet at root2: only 'a' deleted
	results, err = arctable.BatchGet(ctx, bucketId, root2, []string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("BatchGet root2 failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results at root2, got %d", len(results))
	}
	if results["a"] != cid.Undef {
		t.Error("'a' should be deleted at root2")
	}
	if !results["b"].Equals(target2) {
		t.Error("'b' should still exist at root2")
	}

	// BatchGet at root1: all exist
	results, err = arctable.BatchGet(ctx, bucketId, root1, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchGet root1 failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results at root1, got %d", len(results))
	}
	if !results["a"].Equals(target1) {
		t.Error("'a' should exist at root1")
	}
}

func TestVersionedArcTableBatchGetMultipleBuckets(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	root1a := newTestCID([]byte("root1a"))
	root1b := newTestCID([]byte("root1b"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Different buckets
	arctable.Update(ctx, "bucket1", root1a, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target1,
	})
	arctable.Update(ctx, "bucket2", root1b, cid.Undef, map[string]cid.Cid{
		"a": target2,
		"b": target2,
	})

	// BatchGet in different buckets should be independent
	results1, _ := arctable.BatchGet(ctx, "bucket1", root1a, []string{"a", "b"})
	results2, _ := arctable.BatchGet(ctx, "bucket2", root1b, []string{"a", "b"})

	if len(results1) != 2 || len(results2) != 2 {
		t.Error("expected 2 results in each bucket")
	}

	if !results1["a"].Equals(target1) {
		t.Error("bucket1 should have target1")
	}
	if !results2["a"].Equals(target2) {
		t.Error("bucket2 should have target2")
	}
}

func TestVersionedArcTableDeleteViaUpdate(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "delete-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// v1: a, b
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	arctable.Update(ctx, bucketId, root1, cid.Undef, arcs1)

	// v2: delete 'a' using cid.Undef (tombstone)
	arcs2 := map[string]cid.Cid{
		"a": cid.Undef, // tombstone - marks 'a' as deleted
	}
	arctable.Update(ctx, bucketId, root2, root1, arcs2)

	// At root2, 'a' should not be found (tombstone stops the search)
	_, err = arctable.Get(ctx, bucketId, root2, "a")
	if err == nil {
		t.Error("'a' should be deleted at root2")
	}

	// 'b' should still be accessible (from root1)
	got, err := arctable.Get(ctx, bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// At root1, 'a' should still exist (tombstone is at root2, not root1)
	got, err = arctable.Get(ctx, bucketId, root1, "a")
	if err != nil {
		t.Fatalf("Get a at root1 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("a at root1 should be target1")
	}

	// v3: add 'c', tombstone for 'a' should still be effective
	arcs3 := map[string]cid.Cid{
		"c": target1,
	}
	arctable.Update(ctx, bucketId, root3, root2, arcs3)

	// At root3, 'a' should still not be found
	_, err = arctable.Get(ctx, bucketId, root3, "a")
	if err == nil {
		t.Error("'a' should be deleted at root3")
	}

	// 'b' and 'c' should work
	got, err = arctable.Get(ctx, bucketId, root3, "b")
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	got, err = arctable.Get(ctx, bucketId, root3, "c")
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("c at root3 should be target1")
	}
}

func TestVersionedArcTableMultipleBuckets(t *testing.T) {
	kv := memory.New() // Shared KVStore

	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	root1a := newTestCID([]byte("root1a"))
	root2a := newTestCID([]byte("root2a"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create versions in different buckets
	arctable.Update(ctx, "bucket1", root1a, cid.Undef, map[string]cid.Cid{"a": target1})
	arctable.Update(ctx, "bucket2", root2a, cid.Undef, map[string]cid.Cid{"a": target2})

	// Should be independent
	got1, _ := arctable.Get(ctx, "bucket1", root1a, "a")
	got2, _ := arctable.Get(ctx, "bucket2", root2a, "a")

	if got1.Equals(got2) {
		t.Error("different buckets should have independent values")
	}

	if !got1.Equals(target1) {
		t.Error("bucket1 should have target1")
	}

	if !got2.Equals(target2) {
		t.Error("bucket2 should have target2")
	}
}

// === Bloom Filter Tests ===

func TestVersionedArcTableWithBloomCache(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, err := NewArcTableWithBloomCache(kv, bc)
	if err != nil {
		t.Fatalf("NewArcTableWithBloomCache failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create bucket
	err = arctable.CreateBucket(ctx, bucketId, &bloom.BucketConfig{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.01,
	})
	if err != nil {
		t.Fatalf("CreateBucket failed: %v", err)
	}

	// Add arc
	arctable.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{"path/a": target})

	// MightContain should return true for existing path
	if !arctable.MightContain(ctx, bucketId, "path/a") {
		t.Error("MightContain should return true for existing path")
	}
}

func TestVersionedArcTableMightContainBatch(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	bucketId := "batch-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create bucket
	arctable.CreateBucket(ctx, bucketId, nil)

	// Add arcs
	paths := []string{"a", "b", "c"}
	arcs := make(map[string]cid.Cid)
	for _, p := range paths {
		arcs[p] = target
	}
	arctable.Update(ctx, bucketId, root, cid.Undef, arcs)

	// Batch check
	results := arctable.MightContainBatch(ctx, bucketId, []string{"a", "b", "c", "nonexistent"})
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}

	// Existing paths should return true
	for _, p := range paths {
		if !results[p] {
			t.Errorf("expected true for %s", p)
		}
	}
}

func TestVersionedArcTableBloomFilterOptimization(t *testing.T) {
	// Test that bloom filter actually skips kvstore lookup for non-existent paths
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	bucketId := "optimized-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create bucket
	arctable.CreateBucket(ctx, bucketId, nil)

	// Add arcs
	arctable.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{"existing": target})

	// Get for existing path should work
	got, err := arctable.Get(ctx, bucketId, root, "existing")
	if err != nil {
		t.Fatalf("Get existing failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value")
	}

	// Get for path that definitely doesn't exist (bloom says no)
	// should return ErrNotFound without version chain walk
	_, err = arctable.Get(ctx, bucketId, root, "definitely-not-exist")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestVersionedArcTableBloomUpdateOnUpdate(t *testing.T) {
	// Test that bloom filter is updated when arcs are added
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	bucketId := "update-bloom-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Create bucket
	arctable.CreateBucket(ctx, bucketId, nil)

	// First update
	arctable.Update(ctx, bucketId, root1, cid.Undef, map[string]cid.Cid{"a": target})

	// Second update (adds new paths)
	arctable.Update(ctx, bucketId, root2, root1, map[string]cid.Cid{"b": target})

	// Both paths should be in bloom
	if !arctable.MightContain(ctx, bucketId, "a") {
		t.Error("'a' should be in bloom")
	}
	if !arctable.MightContain(ctx, bucketId, "b") {
		t.Error("'b' should be in bloom")
	}
}

func TestVersionedArcTableWithoutBloomCache(t *testing.T) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))

	ctx := context.Background()
	bucketId := "no-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Add arc
	arctable.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{"a": target})

	// CreateBucket should fail (no bloom cache)
	err := arctable.CreateBucket(ctx, bucketId, nil)
	if err == nil {
		t.Error("CreateBucket should fail without bloom cache")
	}

	// MightContain should return true (bloom disabled)
	if !arctable.MightContain(ctx, bucketId, "any-path") {
		t.Error("MightContain should return true when bloom disabled")
	}

	// MightContainBatch should return all true
	results := arctable.MightContainBatch(ctx, bucketId, []string{"a", "b", "c"})
	for p, v := range results {
		if !v {
			t.Errorf("expected true for %s when bloom disabled", p)
		}
	}
}

// === Benchmarks ===

// setupVersionChain creates a chain of versions and returns the latest root
func setupVersionChain(ctx context.Context, arctable *ArcTable, bucketId string, chainLength int) cid.Cid {
	var prevRoot cid.Cid
	var latestRoot cid.Cid

	for i := 0; i < chainLength; i++ {
		root := newTestCID([]byte(fmt.Sprintf("root%d", i)))
		arcs := map[string]cid.Cid{
			fmt.Sprintf("v%d_arc", i): newTestCID([]byte(fmt.Sprintf("target%d", i))),
		}
		arctable.Update(ctx, bucketId, root, prevRoot, arcs)
		prevRoot = root
		latestRoot = root
	}
	return latestRoot
}

func BenchmarkVersionedArcTableGet(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"

	// Test different version chain lengths
	chainLengths := []int{1, 10, 50, 100}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, bucketId, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Query an arc that exists at the first version (requires full chain walk)
				arctable.Get(ctx, bucketId, latestRoot, "v0_arc")
			}
		})
	}
}

func BenchmarkVersionedArcTableGetLatestVersion(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"

	// Test Get performance for arc at latest version (no chain walk needed)
	chainLengths := []int{1, 10, 50, 100}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, bucketId, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Query an arc at the latest version (direct lookup)
				arctable.Get(ctx, bucketId, latestRoot, fmt.Sprintf("v%d_arc", length-1))
			}
		})
	}
}

func BenchmarkVersionedArcTableUpdate(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"

	batchSizes := []int{1, 10, 100}
	for _, size := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
			// Initial setup
			initialRoot := newTestCID([]byte("initial"))
			initialArcs := map[string]cid.Cid{
				"a": newTestCID([]byte("init")),
			}
			arctable.Update(ctx, bucketId, initialRoot, cid.Undef, initialArcs)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				newRoot := newTestCID([]byte(fmt.Sprintf("root%d", i)))
				arcs := make(map[string]cid.Cid)
				for j := 0; j < size; j++ {
					arcs[fmt.Sprintf("arc%d", j)] = newTestCID([]byte(fmt.Sprintf("val%d_%d", i, j)))
				}
				arctable.Update(ctx, bucketId, newRoot, initialRoot, arcs)
			}
		})
	}
}

func BenchmarkVersionedArcTableSnapshot(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"

	chainLengths := []int{1, 10, 20}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, bucketId, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot, _ := arctable.Snapshot(ctx, bucketId, latestRoot)
				snapshot.Get(arcset.CanonicalizePath("v0_arc"))
			}
		})
	}
}

func BenchmarkVersionedArcTableIterate(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"

	chainLengths := []int{1, 10, 20}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, bucketId, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				iter := arctable.Iterate(ctx, bucketId, latestRoot)
				for {
					_, _, ok := iter.Next()
					if !ok {
						break
					}
				}
				iter.Close()
			}
		})
	}
}

func BenchmarkVersionedArcTableGetParent(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	bucketId := "bench-graph"

	chainLengths := []int{10, 50, 100}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			ctx := context.Background()
			latestRoot := setupVersionChain(ctx, arctable, bucketId, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				arctable.GetParent(ctx, bucketId, latestRoot)
			}
		})
	}
}
