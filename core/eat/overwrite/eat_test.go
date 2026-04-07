package overwrite

import (
	"fmt"
	"testing"

	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
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
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}
	if eat == nil {
		t.Error("eat should not be nil")
	}

	// Nil KVStore
	_, err = NewEAT(nil)
	if err == nil {
		t.Error("expected error for nil KVStore")
	}
}

func TestEATUpdateAndGet(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

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
	err = eat.Update(bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root1
	got, err := eat.Get(bucketId, root1, "a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, err = eat.Get(bucketId, root1, "b")
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
	err = eat.Update(bucketId, root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root2 should work
	got, err = eat.Get(bucketId, root2, "a")
	if err != nil {
		t.Fatalf("Get via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'a' via root2")
	}

	got, err = eat.Get(bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b via root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("'b' should still be target2")
	}

	got, err = eat.Get(bucketId, root2, "c")
	if err != nil {
		t.Fatalf("Get c via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'c' via root2")
	}

	// Old root1 should no longer work
	_, err = eat.Get(bucketId, root1, "a")
	if err == nil {
		t.Error("old root should no longer work after update")
	}
}

func TestEATGetWithoutRoot(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "test-bucket"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Store arc
	eat.Update(bucketId, root, cid.Undef, map[string]cid.Cid{"a": target})

	// Get with root validation
	got, err := eat.Get(bucketId, root, "a")
	if err != nil {
		t.Fatalf("Get with root failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value")
	}

	// Get without root validation (root = cid.Undef)
	got, err = eat.Get(bucketId, cid.Undef, "a")
	if err != nil {
		t.Fatalf("Get without root failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value without root")
	}
}

func TestEATDeleteViaUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "delete-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Setup
	eat.Update(bucketId, root1, cid.Undef, map[string]cid.Cid{
		"a": target,
		"b": target,
	})

	// Delete 'a' using cid.Undef
	err = eat.Update(bucketId, root2, root1, map[string]cid.Cid{
		"a": cid.Undef, // delete
	})
	if err != nil {
		t.Fatalf("Update with delete failed: %v", err)
	}

	// 'a' should be gone
	_, err = eat.Get(bucketId, root2, "a")
	if err == nil {
		t.Error("'a' should be deleted")
	}

	// 'b' should still exist
	got, err := eat.Get(bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("'b' should still exist")
	}
}

func TestEATSnapshot(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "snapshot-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	eat.Update(bucketId, root, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
	})

	snapshot := eat.Snapshot(bucketId, root)

	got, ok := snapshot.Get("a")
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
	emptySnapshot := eat.Snapshot(bucketId, invalidRoot)
	if emptySnapshot.Len() != 0 {
		t.Error("invalid root should return empty snapshot")
	}
}

func TestEATIterate(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "iterate-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	eat.Update(bucketId, root, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
	})

	iter := eat.Iterate(bucketId, root)
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

	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Same path, different buckets
	eat.Update("bucket1", root1, cid.Undef, map[string]cid.Cid{"key": target1})
	eat.Update("bucket2", root2, cid.Undef, map[string]cid.Cid{"key": target2})

	// Should be independent
	got1, _ := eat.Get("bucket1", root1, "key")
	got2, _ := eat.Get("bucket2", root2, "key")

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
	if eat.Snapshot("bucket1", root1).Len() != 1 {
		t.Error("bucket1.Snapshot.Len should be 1")
	}
	if eat.Snapshot("bucket2", root2).Len() != 1 {
		t.Error("bucket2.Snapshot.Len should be 1")
	}
}

func TestEATClear(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "clear-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))
	eat.Update(bucketId, root, cid.Undef, map[string]cid.Cid{
		"a": target,
		"b": target,
	})

	err = eat.Clear(bucketId)
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// After clear, the bucket should be empty (snapshot returns empty)
	// Note: Clear only clears arc data, not root mappings, so we check without root validation
	snapshot := eat.Snapshot(bucketId, cid.Undef)
	if snapshot.Len() != 0 {
		t.Errorf("expected empty after clear, got %d", snapshot.Len())
	}
}

func TestEATBatchUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "batch-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	// Large batch
	arcs1 := make(map[string]cid.Cid)
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("arc%d", i)
		arcs1[path] = newTestCID([]byte(path))
	}

	err = eat.Update(bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if eat.Snapshot(bucketId, root1).Len() != 100 {
		t.Errorf("expected 100 arcs, got %d", eat.Snapshot(bucketId, root1).Len())
	}

	// Second batch (partial overwrite)
	arcs2 := make(map[string]cid.Cid)
	for i := 50; i < 150; i++ {
		path := fmt.Sprintf("arc%d", i)
		arcs2[path] = newTestCID([]byte("new_" + path))
	}

	err = eat.Update(bucketId, root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update 2 failed: %v", err)
	}

	// Should have 150 arcs (0-149)
	if eat.Snapshot(bucketId, root2).Len() != 150 {
		t.Errorf("expected 150 arcs after second update, got %d", eat.Snapshot(bucketId, root2).Len())
	}

	// Verify old root doesn't work
	_, err = eat.Get(bucketId, root1, "arc0")
	if err == nil {
		t.Error("old root should not work")
	}

	// Verify new root works
	got, err := eat.Get(bucketId, root2, "arc0")
	if err != nil {
		t.Fatalf("Get arc0 via root2 failed: %v", err)
	}
	// arc0 should still have original value (not in arcs2)
	if !got.Equals(arcs1["arc0"]) {
		t.Error("arc0 should have original value")
	}

	// arc50 should have new value
	got, err = eat.Get(bucketId, root2, "arc50")
	if err != nil {
		t.Fatalf("Get arc50 via root2 failed: %v", err)
	}
	if !got.Equals(arcs2["arc50"]) {
		t.Error("arc50 should have new value")
	}
}

// === Benchmarks ===

func BenchmarkOverwriteEATGet(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(kv)
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
			eat.Update(bucketId, root, cid.Undef, arcs)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				path := fmt.Sprintf("arc%d", i%count)
				eat.Get(bucketId, root, path)
			}
		})
	}
}

func BenchmarkOverwriteEATUpdate(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(kv)
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
				eat.Update(bucketId, root, cid.Undef, arcs)
			}
		})
	}
}

func BenchmarkOverwriteEATSnapshot(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(kv)
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
			eat.Update(bucketId, root, cid.Undef, arcs)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot := eat.Snapshot(bucketId, root)
				snapshot.Get("arc0")
			}
		})
	}
}

func BenchmarkOverwriteEATIterate(b *testing.B) {
	kv := kvstore_memory.New()
	eat, _ := NewEAT(kv)
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
			eat.Update(bucketId, root, cid.Undef, arcs)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				iter := eat.Iterate(bucketId, root)
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

