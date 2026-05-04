package overwrite

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/bloom"
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

func testPathSlice(paths []string) []arcset.Path {
	out := make([]arcset.Path, len(paths))
	for i, path := range paths {
		out[i] = arcset.CanonicalizePath(path)
	}
	return out
}

// === ArcTable Tests ===

func TestArcTableNew(t *testing.T) {
	kv := kvstore_memory.New()

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

func TestArcTableUpdateAndGet(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "mygraph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// First update (no old root)
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(arcs1))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root1
	got, err := arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, err = arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("b"))
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
	err = arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(arcs2))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root2 should work
	got, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'a' via root2")
	}

	got, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("Get b via root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("'b' should still be target2")
	}

	got, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("c"))
	if err != nil {
		t.Fatalf("Get c via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'c' via root2")
	}

	// Old root1 should no longer work
	_, err = arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("a"))
	if err == nil {
		t.Error("old root should no longer work after update")
	}
}

func TestArcTableGetWithoutRoot(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "test-namespace"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Store arc
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"a": target}))

	// Get with root validation
	got, err := arctable.Get(ctx, namespace, root, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get with root failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value")
	}

	// Get without root validation (root = cid.Undef)
	got, err = arctable.Get(ctx, namespace, cid.Undef, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get without root failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value without root")
	}
}

func TestArcTableDeleteViaUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "delete-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Setup
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target,
		"b": target,
	}))

	// Delete 'a' using cid.Undef
	err = arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{
		"a": cid.Undef, // delete
	}))
	if err != nil {
		t.Fatalf("Update with delete failed: %v", err)
	}

	// 'a' should be gone
	_, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("a"))
	if err == nil {
		t.Error("'a' should be deleted")
	}

	// 'b' should still exist
	got, err := arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("Get b failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("'b' should still exist")
	}
}

func TestArcTableSnapshot(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "snapshot-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}))

	snapshot, err := arctable.Snapshot(ctx, namespace, root)
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
	emptySnapshot, err := arctable.Snapshot(ctx, namespace, invalidRoot)
	if err != nil {
		t.Fatalf("Snapshot with invalid root should not error: %v", err)
	}
	if emptySnapshot.Len() != 0 {
		t.Error("invalid root should return empty snapshot")
	}
}

func TestArcTableIterate(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "iterate-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}))

	iter := arctable.Iterate(ctx, namespace, root)
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

func TestArcTableMultipleNamespaces(t *testing.T) {
	kv := kvstore_memory.New() // Shared KVStore

	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Same path, different namespaces
	arctable.Update(ctx, "namespace1", root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"key": target1}))
	arctable.Update(ctx, "namespace2", root2, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"key": target2}))

	// Should be independent
	got1, _ := arctable.Get(ctx, "namespace1", root1, arcset.CanonicalizePath("key"))
	got2, _ := arctable.Get(ctx, "namespace2", root2, arcset.CanonicalizePath("key"))

	if got1.Equals(got2) {
		t.Error("different namespaces should have independent values")
	}

	if !got1.Equals(target1) {
		t.Error("namespace1 should have target1")
	}

	if !got2.Equals(target2) {
		t.Error("namespace2 should have target2")
	}

	// Snapshot should be per-namespace
	snapshot1, err := arctable.Snapshot(ctx, "namespace1", root1)
	if err != nil {
		t.Fatalf("Snapshot namespace1 failed: %v", err)
	}
	if snapshot1.Len() != 1 {
		t.Error("namespace1.Snapshot.Len should be 1")
	}
	snapshot2, err := arctable.Snapshot(ctx, "namespace2", root2)
	if err != nil {
		t.Fatalf("Snapshot namespace2 failed: %v", err)
	}
	if snapshot2.Len() != 1 {
		t.Error("namespace2.Snapshot.Len should be 1")
	}
}

func TestArcTableBatchGet(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-graph"
	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// Setup arcs
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	}))

	// Test: all paths found
	results, err := arctable.BatchGet(ctx, namespace, root, testPathSlice([]string{"a", "b", "c"}))
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
	results, err = arctable.BatchGet(ctx, namespace, root, testPathSlice([]string{"a", "notexist", "b"}))
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
	results, err = arctable.BatchGet(ctx, namespace, root, testPathSlice([]string{}))
	if err != nil {
		t.Fatalf("BatchGet with empty paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty paths, got %d", len(results))
	}

	// Test: all paths not found
	results, err = arctable.BatchGet(ctx, namespace, root, testPathSlice([]string{"x", "y", "z"}))
	if err != nil {
		t.Fatalf("BatchGet with all missing paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}

	// Test: without root validation
	results, err = arctable.BatchGet(ctx, namespace, cid.Undef, testPathSlice([]string{"a", "b"}))
	if err != nil {
		t.Fatalf("BatchGet without root failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results without root, got %d", len(results))
	}

	// Test: invalid root
	invalidRoot := newTestCID([]byte("invalid"))
	results, err = arctable.BatchGet(ctx, namespace, invalidRoot, testPathSlice([]string{"a", "b"}))
	if err == nil {
		t.Error("expected error for invalid root")
	}
}

func TestArcTableBatchGetAfterUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-update-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// First version
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}))

	// Second version (overwrites 'a', adds 'c')
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target3,
		"c": target3,
	}))

	// BatchGet with root2 should see updated values
	results, err := arctable.BatchGet(ctx, namespace, root2, testPathSlice([]string{"a", "b", "c"}))
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
	results, err = arctable.BatchGet(ctx, namespace, root1, testPathSlice([]string{"a"}))
	if err == nil {
		t.Error("old root should not work after update")
	}
}

func TestArcTableBatchGetAfterDelete(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-delete-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Setup
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target,
		"b": target,
		"c": target,
	}))

	// Delete 'a' and 'b'
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{
		"a": cid.Undef,
		"b": cid.Undef,
	}))

	// BatchGet should only return 'c'
	results, err := arctable.BatchGet(ctx, namespace, root2, testPathSlice([]string{"a", "b", "c"}))
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

func TestArcTableBatchUpdate(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batch-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	// Large batch
	arcs1 := make(map[string]cid.Cid)
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("arc%d", i)
		arcs1[path] = newTestCID([]byte(path))
	}

	err = arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(arcs1))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	snapshot1, err := arctable.Snapshot(ctx, namespace, root1)
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

	err = arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(arcs2))
	if err != nil {
		t.Fatalf("Update 2 failed: %v", err)
	}

	// Should have 150 arcs (0-149)
	snapshot2, err := arctable.Snapshot(ctx, namespace, root2)
	if err != nil {
		t.Fatalf("Snapshot root2 failed: %v", err)
	}
	if snapshot2.Len() != 150 {
		t.Errorf("expected 150 arcs after second update, got %d", snapshot2.Len())
	}

	// Verify old root doesn't work
	_, err = arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("arc0"))
	if err == nil {
		t.Error("old root should not work")
	}

	// Verify new root works
	got, err := arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("arc0"))
	if err != nil {
		t.Fatalf("Get arc0 via root2 failed: %v", err)
	}
	// arc0 should still have original value (not in arcs2)
	if !got.Equals(arcs1["arc0"]) {
		t.Error("arc0 should have original value")
	}

	// arc50 should have new value
	got, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("arc50"))
	if err != nil {
		t.Fatalf("Get arc50 via root2 failed: %v", err)
	}
	if !got.Equals(arcs2["arc50"]) {
		t.Error("arc50 should have new value")
	}
}

// === Bloom Filter Tests ===

func TestArcTableWithBloomCache(t *testing.T) {
	kv := kvstore_memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, err := NewArcTableWithBloomCache(kv, bc)
	if err != nil {
		t.Fatalf("NewArcTableWithBloomCache failed: %v", err)
	}

	ctx := context.Background()
	namespace := "bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create namespace
	err = arctable.CreateNamespace(ctx, namespace, &bloom.NamespaceConfig{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.01,
	})
	if err != nil {
		t.Fatalf("CreateNamespace failed: %v", err)
	}

	// Add arc
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"path/a": target}))

	// MightContain should return true for existing path
	if !arctable.MightContain(ctx, namespace, arcset.CanonicalizePath("path/a")) {
		t.Error("MightContain should return true for existing path")
	}

	// MightContain may return true or false for non-existing path
	// (bloom filter allows false positives)
	_ = arctable.MightContain(ctx, namespace, arcset.CanonicalizePath("nonexistent/path"))
}

func TestArcTableMightContainBatch(t *testing.T) {
	kv := kvstore_memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	namespace := "batch-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create namespace
	arctable.CreateNamespace(ctx, namespace, nil)

	// Add arcs
	paths := []string{"a", "b", "c"}
	arcs := make(map[string]cid.Cid)
	for _, p := range paths {
		arcs[p] = target
	}
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(arcs))

	// Batch check
	results := arctable.MightContainBatch(ctx, namespace, testPathSlice([]string{"a", "b", "c", "nonexistent"}))
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}

	// Existing paths should return true
	for _, p := range paths {
		if !results[arcset.CanonicalizePath(p)] {
			t.Errorf("expected true for %s", p)
		}
	}
}

func TestArcTableBloomFilterOptimization(t *testing.T) {
	// Test that bloom filter actually skips kvstore lookup for non-existent paths
	kv := kvstore_memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	namespace := "optimized-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create namespace
	arctable.CreateNamespace(ctx, namespace, nil)

	// Add arcs
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"existing": target}))

	// Get for existing path should work
	got, err := arctable.Get(ctx, namespace, root, arcset.CanonicalizePath("existing"))
	if err != nil {
		t.Fatalf("Get existing failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value")
	}

	// Get for path that definitely doesn't exist (bloom says no)
	// should return ErrNotFound without kvstore lookup
	_, err = arctable.Get(ctx, namespace, root, arcset.CanonicalizePath("definitely-not-exist"))
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestArcTableWithoutBloomCache(t *testing.T) {
	kv := kvstore_memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))

	ctx := context.Background()
	namespace := "no-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Add arc
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"a": target}))

	// CreateNamespace should fail (no bloom cache)
	err := arctable.CreateNamespace(ctx, namespace, nil)
	if err == nil {
		t.Error("CreateNamespace should fail without bloom cache")
	}

	// MightContain should return true (bloom disabled)
	if !arctable.MightContain(ctx, namespace, arcset.CanonicalizePath("any-path")) {
		t.Error("MightContain should return true when bloom disabled")
	}

	// MightContainBatch should return all true
	results := arctable.MightContainBatch(ctx, namespace, testPathSlice([]string{"a", "b", "c"}))
	for p, v := range results {
		if !v {
			t.Errorf("expected true for %s when bloom disabled", p)
		}
	}
}

// === Benchmarks ===

func BenchmarkOverwriteArcTableGet(b *testing.B) {
	kv := kvstore_memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"
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
			arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(arcs))

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				path := fmt.Sprintf("arc%d", i%count)
				arctable.Get(ctx, namespace, root, arcset.CanonicalizePath(path))
			}
		})
	}
}

func BenchmarkOverwriteArcTableUpdate(b *testing.B) {
	kv := kvstore_memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"

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
				arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(arcs))
			}
		})
	}
}

func BenchmarkOverwriteArcTableSnapshot(b *testing.B) {
	kv := kvstore_memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"
	root := newTestCID([]byte("root"))

	arcCounts := []int{10, 100, 1000}
	for _, count := range arcCounts {
		b.Run(fmt.Sprintf("arcs_%d", count), func(b *testing.B) {
			arcs := make(map[string]cid.Cid)
			for i := 0; i < count; i++ {
				path := fmt.Sprintf("arc%d", i)
				arcs[path] = newTestCID([]byte(path))
			}
			arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(arcs))

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot, _ := arctable.Snapshot(ctx, namespace, root)
				snapshot.Get(arcset.CanonicalizePath("arc0"))
			}
		})
	}
}

func BenchmarkOverwriteArcTableIterate(b *testing.B) {
	kv := kvstore_memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"
	root := newTestCID([]byte("root"))

	arcCounts := []int{10, 100, 1000}
	for _, count := range arcCounts {
		b.Run(fmt.Sprintf("arcs_%d", count), func(b *testing.B) {
			arcs := make(map[string]cid.Cid)
			for i := 0; i < count; i++ {
				path := fmt.Sprintf("arc%d", i)
				arcs[path] = newTestCID([]byte(path))
			}
			arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(arcs))

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				iter := arctable.Iterate(ctx, namespace, root)
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
