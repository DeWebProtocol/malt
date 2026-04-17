package overwrite

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/eat/bloom"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
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

// === EAT Tests ===

func TestEATNew(t *testing.T) {
	kv := kvstore_memory.New()

	// Valid creation
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}
	if eat == nil {
		t.Error("eat should not be nil")
	}

	// Nil KVStore
	_, err = NewEAT()
	if err == nil {
		t.Error("expected error for nil KVStore")
	}
}

func TestEATUpdateAndGet(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "mygraph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// First update (no old root)
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = eat.Update(ctx, bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root1
	got, err := eat.Get(ctx, bucketId, root1, "a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, err = eat.Get(ctx, bucketId, root1, "b")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}

	// Update with new root (overwrites 'a', adds 'c')
	target3 := newTestCID([]byte("target3"))
	arcs2 := map[string]cid.Cid{
		"a": target3, // overwrite
		"c": target3, // new
	}
	err = eat.Update(ctx, bucketId, root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root2 should work
	got, err = eat.Get(ctx, bucketId, root2, "a")
	if err != nil {
		t.Fatalf("Get via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'a' via root2")
	}

	got, err = eat.Get(ctx, bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b via root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("'b' should still be target2")
	}

	got, err = eat.Get(ctx, bucketId, root2, "c")
	if err != nil {
		t.Fatalf("Get c via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'c' via root2")
	}

	// Old root1 should no longer work
	_, err = eat.Get(ctx, bucketId, root1, "a")
	if err == nil {
		t.Error("old root should no longer work after update")
	}
}

func TestEATGetWithoutRoot(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "test-bucket"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Store arc
	eat.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{"a": target})

	// Get with root validation
	got, err := eat.Get(ctx, bucketId, root, "a")
	if err != nil {
		t.Fatalf("Get with root failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value")
	}

	// Get without root validation (root = cid.Undef)
	got, err = eat.Get(ctx, bucketId, cid.Undef, "a")
	if err != nil {
		t.Fatalf("Get without root failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value without root")
	}
}

func TestEATDeleteViaUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "delete-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Setup
	eat.Update(ctx, bucketId, root1, cid.Undef, map[string]cid.Cid{
		"a": target,
		"b": target,
	})

	// Delete 'a' using cid.Undef
	err = eat.Update(ctx, bucketId, root2, root1, map[string]cid.Cid{
		"a": cid.Undef, // delete
	})
	if err != nil {
		t.Fatalf("Update with delete failed: %v", err)
	}

	// 'a' should be gone
	_, err = eat.Get(ctx, bucketId, root2, "a")
	if err == nil {
		t.Error("'a' should be deleted")
	}

	// 'b' should still exist
	got, err := eat.Get(ctx, bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("'b' should still exist")
	}
}

func TestEATSnapshot(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "snapshot-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	eat.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
	})

	snapshot, err := eat.Snapshot(ctx, bucketId, root)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	got, ok := snapshot.Get(arcset.CanonicalizePath("a"))
	if !ok {
		t.Error("expected to find 'a'")
	}
	if !got.Equals(target1) {
		t.Error("wrong value from snapshot")
	}

	if snapshot.Len() != 2 {
		t.Errorf("expected Len 2, got %d", snapshot.Len())
	}

	// Snapshot with invalid root should return empty snapshot
	invalidRoot := newTestCID([]byte("invalid"))
	emptySnapshot, err := eat.Snapshot(ctx, bucketId, invalidRoot)
	if err != nil {
		t.Fatalf("Snapshot with invalid root should not error: %v", err)
	}
	if emptySnapshot.Len() != 0 {
		t.Error("invalid root should return empty snapshot")
	}
}

func TestEATIterate(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "iterate-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	eat.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
	})

	iter := eat.Iterate(ctx, bucketId, root)
	defer iter.Close()

	count := 0
	for {
		_, _, ok := iter.Next()
		if !ok {
			break
		}
		count++
	}

	if count != 2 {
		t.Errorf("expected 2 arcs, got %d", count)
	}
}

func TestEATMultipleBuckets(t *testing.T) {
	kv := kvstore_memory.New() // Shared KVStore

	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Same path, different buckets
	eat.Update(ctx, "bucket1", root1, cid.Undef, map[string]cid.Cid{"key": target1})
	eat.Update(ctx, "bucket2", root2, cid.Undef, map[string]cid.Cid{"key": target2})

	// Should be independent
	got1, _ := eat.Get(ctx, "bucket1", root1, "key")
	got2, _ := eat.Get(ctx, "bucket2", root2, "key")

	if got1.Equals(got2) {
		t.Error("different buckets should have independent values")
	}

	if !got1.Equals(target1) {
		t.Error("bucket1 should have target1")
	}

	if !got2.Equals(target2) {
		t.Error("bucket2 should have target2")
	}

	// Snapshot should be per-bucket
	snapshot1, err := eat.Snapshot(ctx, "bucket1", root1)
	if err != nil {
		t.Fatalf("Snapshot bucket1 failed: %v", err)
	}
	if snapshot1.Len() != 1 {
		t.Error("bucket1.Snapshot.Len should be 1")
	}
	snapshot2, err := eat.Snapshot(ctx, "bucket2", root2)
	if err != nil {
		t.Fatalf("Snapshot bucket2 failed: %v", err)
	}
	if snapshot2.Len() != 1 {
		t.Error("bucket2.Snapshot.Len should be 1")
	}
}


func TestEATBatchGet(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "batchget-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// Setup arcs
	eat.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	})

	// Test: all paths found
	results, err := eat.BatchGet(ctx, bucketId, root, []string{"a", "b", "c"})
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
	results, err = eat.BatchGet(ctx, bucketId, root, []string{"a", "notexist", "b"})
	if err != nil {
		t.Fatalf("BatchGet with missing paths failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (missing path omitted), got %d", len(results))
	}
	if results["notexist"] != cid.Undef {
		t.Error("missing path should not be in results")
	}

	// Test: empty paths
	results, err = eat.BatchGet(ctx, bucketId, root, []string{})
	if err != nil {
		t.Fatalf("BatchGet with empty paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty paths, got %d", len(results))
	}

	// Test: all paths not found
	results, err = eat.BatchGet(ctx, bucketId, root, []string{"x", "y", "z"})
	if err != nil {
		t.Fatalf("BatchGet with all missing paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}

	// Test: without root validation
	results, err = eat.BatchGet(ctx, bucketId, cid.Undef, []string{"a", "b"})
	if err != nil {
		t.Fatalf("BatchGet without root failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results without root, got %d", len(results))
	}

	// Test: invalid root
	invalidRoot := newTestCID([]byte("invalid"))
	results, err = eat.BatchGet(ctx, bucketId, invalidRoot, []string{"a", "b"})
	if err == nil {
		t.Error("expected error for invalid root")
	}
}

func TestEATBatchGetAfterUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "batchget-update-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// First version
	eat.Update(ctx, bucketId, root1, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
	})

	// Second version (overwrites 'a', adds 'c')
	eat.Update(ctx, bucketId, root2, root1, map[string]cid.Cid{
		"a": target3,
		"c": target3,
	})

	// BatchGet with root2 should see updated values
	results, err := eat.BatchGet(ctx, bucketId, root2, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchGet root2 failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	if !results["a"].Equals(target3) {
		t.Error("'a' should be target3 (overwritten)")
	}
	if !results["b"].Equals(target2) {
		t.Error("'b' should still be target2")
	}
	if !results["c"].Equals(target3) {
		t.Error("'c' should be target3 (new)")
	}

	// BatchGet with old root1 should fail
	results, err = eat.BatchGet(ctx, bucketId, root1, []string{"a"})
	if err == nil {
		t.Error("old root should not work after update")
	}
}

func TestEATBatchGetAfterDelete(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "batchget-delete-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Setup
	eat.Update(ctx, bucketId, root1, cid.Undef, map[string]cid.Cid{
		"a": target,
		"b": target,
		"c": target,
	})

	// Delete 'a' and 'b'
	eat.Update(ctx, bucketId, root2, root1, map[string]cid.Cid{
		"a": cid.Undef,
		"b": cid.Undef,
	})

	// BatchGet should only return 'c'
	results, err := eat.BatchGet(ctx, bucketId, root2, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BatchGet after delete failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (a and b deleted), got %d", len(results))
	}
	if !results["c"].Equals(target) {
		t.Error("'c' should still exist")
	}
}


func TestEATBatchUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "batch-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	// Large batch
	arcs1 := make(map[string]cid.Cid)
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("arc%d", i)
		arcs1[path] = newTestCID([]byte(path))
	}

	err = eat.Update(ctx, bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	snapshot1, err := eat.Snapshot(ctx, bucketId, root1)
	if err != nil {
		t.Fatalf("Snapshot root1 failed: %v", err)
	}
	if snapshot1.Len() != 100 {
		t.Errorf("expected 100 arcs, got %d", snapshot1.Len())
	}

	// Second batch (partial overwrite)
	arcs2 := make(map[string]cid.Cid)
	for i := 50; i < 150; i++ {
		path := fmt.Sprintf("arc%d", i)
		arcs2[path] = newTestCID([]byte("new_" + path))
	}

	err = eat.Update(ctx, bucketId, root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update 2 failed: %v", err)
	}

	// Should have 150 arcs (0-149)
	snapshot2, err := eat.Snapshot(ctx, bucketId, root2)
	if err != nil {
		t.Fatalf("Snapshot root2 failed: %v", err)
	}
	if snapshot2.Len() != 150 {
		t.Errorf("expected 150 arcs after second update, got %d", snapshot2.Len())
	}

	// Verify old root doesn't work
	_, err = eat.Get(ctx, bucketId, root1, "arc0")
	if err == nil {
		t.Error("old root should not work")
	}

	// Verify new root works
	got, err := eat.Get(ctx, bucketId, root2, "arc0")
	if err != nil {
		t.Fatalf("Get arc0 via root2 failed: %v", err)
	}
	// arc0 should still have original value (not in arcs2)
	if !got.Equals(arcs1["arc0"]) {
		t.Error("arc0 should have original value")
	}

	// arc50 should have new value
	got, err = eat.Get(ctx, bucketId, root2, "arc50")
	if err != nil {
		t.Fatalf("Get arc50 via root2 failed: %v", err)
	}
	if !got.Equals(arcs2["arc50"]) {
		t.Error("arc50 should have new value")
	}
}

// === Bloom Filter Tests ===

func TestEATWithBloomCache(t *testing.T) {
	kv := kvstore_memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	eat, err := NewEATWithBloomCache(kv, bc)
	if err != nil {
		t.Fatalf("NewEATWithBloomCache failed: %v", err)
	}

	ctx := context.Background()
	bucketId := "bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create bucket
	err = eat.CreateBucket(ctx, bucketId, &bloom.BucketConfig{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.01,
	})
	if err != nil {
		t.Fatalf("CreateBucket failed: %v", err)
	}

	// Add arc
	eat.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{"path/a": target})

	// MightContain should return true for existing path
	if !eat.MightContain(ctx, bucketId, "path/a") {
		t.Error("MightContain should return true for existing path")
	}

	// MightContain may return true or false for non-existing path
	// (bloom filter allows false positives)
	_ = eat.MightContain(ctx, bucketId, "nonexistent/path")
}

func TestEATMightContainBatch(t *testing.T) {
	kv := kvstore_memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	eat, _ := NewEATWithBloomCache(kv, bc)

	ctx := context.Background()
	bucketId := "batch-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create bucket
	eat.CreateBucket(ctx, bucketId, nil)

	// Add arcs
	paths := []string{"a", "b", "c"}
	arcs := make(map[string]cid.Cid)
	for _, p := range paths {
		arcs[p] = target
	}
	eat.Update(ctx, bucketId, root, cid.Undef, arcs)

	// Batch check
	results := eat.MightContainBatch(ctx, bucketId, []string{"a", "b", "c", "nonexistent"})
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

func TestEATBloomFilterOptimization(t *testing.T) {
	// Test that bloom filter actually skips kvstore lookup for non-existent paths
	kv := kvstore_memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	eat, _ := NewEATWithBloomCache(kv, bc)

	ctx := context.Background()
	bucketId := "optimized-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create bucket
	eat.CreateBucket(ctx, bucketId, nil)

	// Add arcs
	eat.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{"existing": target})

	// Get for existing path should work
	got, err := eat.Get(ctx, bucketId, root, "existing")
	if err != nil {
		t.Fatalf("Get existing failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value")
	}

	// Get for path that definitely doesn't exist (bloom says no)
	// should return ErrNotFound without kvstore lookup
	_, err = eat.Get(ctx, bucketId, root, "definitely-not-exist")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestEATWithoutBloomCache(t *testing.T) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(WithKVStore(kv))

	ctx := context.Background()
	bucketId := "no-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Add arc
	eat.Update(ctx, bucketId, root, cid.Undef, map[string]cid.Cid{"a": target})

	// CreateBucket should fail (no bloom cache)
	err := eat.CreateBucket(ctx, bucketId, nil)
	if err == nil {
		t.Error("CreateBucket should fail without bloom cache")
	}

	// MightContain should return true (bloom disabled)
	if !eat.MightContain(ctx, bucketId, "any-path") {
		t.Error("MightContain should return true when bloom disabled")
	}

	// MightContainBatch should return all true
	results := eat.MightContainBatch(ctx, bucketId, []string{"a", "b", "c"})
	for p, v := range results {
		if !v {
			t.Errorf("expected true for %s when bloom disabled", p)
		}
	}
}

// === Benchmarks ===

func BenchmarkOverwriteEATGet(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"
	root := newTestCID([]byte("root"))

	// Setup: create arcs
	arcCounts := []int{10, 100, 1000}
	for _, count := range arcCounts {
		b.Run(fmt.Sprintf("arcs_%d", count), func(b *testing.B) {
			arcs := make(map[string]cid.Cid)
			for i := 0; i < count; i++ {
				path := fmt.Sprintf("arc%d", i)
				arcs[path] = newTestCID([]byte(path))
			}
			eat.Update(ctx, bucketId, root, cid.Undef, arcs)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				path := fmt.Sprintf("arc%d", i%count)
				eat.Get(ctx, bucketId, root, path)
			}
		})
	}
}

func BenchmarkOverwriteEATUpdate(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"

	batchSizes := []int{1, 10, 100, 1000}
	for _, size := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
			arcs := make(map[string]cid.Cid)
			for i := 0; i < size; i++ {
				path := fmt.Sprintf("arc%d", i)
				arcs[path] = newTestCID([]byte(path))
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root := newTestCID([]byte(fmt.Sprintf("root%d", i)))
				eat.Update(ctx, bucketId, root, cid.Undef, arcs)
			}
		})
	}
}

func BenchmarkOverwriteEATSnapshot(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"
	root := newTestCID([]byte("root"))

	arcCounts := []int{10, 100, 1000}
	for _, count := range arcCounts {
		b.Run(fmt.Sprintf("arcs_%d", count), func(b *testing.B) {
			arcs := make(map[string]cid.Cid)
			for i := 0; i < count; i++ {
				path := fmt.Sprintf("arc%d", i)
				arcs[path] = newTestCID([]byte(path))
			}
			eat.Update(ctx, bucketId, root, cid.Undef, arcs)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot, _ := eat.Snapshot(ctx, bucketId, root)
				snapshot.Get(arcset.CanonicalizePath("arc0"))
			}
		})
	}
}

func BenchmarkOverwriteEATIterate(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(WithKVStore(kv))
	ctx := context.Background()
	bucketId := "bench-graph"
	root := newTestCID([]byte("root"))

	arcCounts := []int{10, 100, 1000}
	for _, count := range arcCounts {
		b.Run(fmt.Sprintf("arcs_%d", count), func(b *testing.B) {
			arcs := make(map[string]cid.Cid)
			for i := 0; i < count; i++ {
				path := fmt.Sprintf("arc%d", i)
				arcs[path] = newTestCID([]byte(path))
			}
			eat.Update(ctx, bucketId, root, cid.Undef, arcs)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				iter := eat.Iterate(ctx, bucketId, root)
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
