package memory

import (
	"testing"

	cid "github.com/ipfs/go-cid"
)

// === BucketedInMemoryEAT Tests ===

func TestBucketedInMemoryEATPutAndGet(t *testing.T) {
	eat := NewBucketedInMemoryEAT()

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Put
	err := eat.Put(root1, "a", target1)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	err = eat.Put(root1, "b", target2)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	err = eat.Put(root2, "x", target1)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get from different buckets
	got, err := eat.Get(root1, "a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value")
	}

	got, err = eat.Get(root2, "x")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value")
	}

	// Non-existent bucket
	_, err = eat.Get(newTestCID([]byte("unknown")), "a")
	if err == nil {
		t.Error("expected error for unknown bucket")
	}

	// Non-existent path in existing bucket
	_, err = eat.Get(root1, "unknown")
	if err == nil {
		t.Error("expected error for unknown path")
	}
}

func TestBucketedInMemoryEATDelete(t *testing.T) {
	eat := NewBucketedInMemoryEAT()

	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	eat.Put(root, "a", target)

	err := eat.Delete(root, "a")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = eat.Get(root, "a")
	if err == nil {
		t.Error("expected error after delete")
	}

	// Delete from non-existent bucket should return ErrNotFound
	err = eat.Delete(newTestCID([]byte("unknown")), "x")
	if err == nil {
		t.Error("delete from non-existent bucket should return error")
	}
}

func TestBucketedInMemoryEATView(t *testing.T) {
	eat := NewBucketedInMemoryEAT()

	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	eat.Put(root, "a", target1)
	eat.Put(root, "b", target2)

	view := eat.View(root)

	got, ok := view.Get("a")
	if !ok {
		t.Error("expected to find 'a'")
	}
	if !got.Equals(target1) {
		t.Error("wrong value")
	}

	// View implements arcset.View
	if view.Len() != 2 {
		t.Errorf("expected Len 2, got %d", view.Len())
	}

	// View for non-existent bucket returns empty view
	emptyView := eat.View(newTestCID([]byte("unknown")))
	if emptyView.Len() != 0 {
		t.Error("expected empty view for unknown bucket")
	}
}

func TestBucketedInMemoryEATClose(t *testing.T) {
	eat := NewBucketedInMemoryEAT()

	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	eat.Put(root, "a", target)

	err := eat.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// After close, operations should fail or return errors
	_, err = eat.Get(root, "a")
	if err == nil {
		t.Error("expected error after close")
	}
}

func TestBucketedInMemoryEATMultipleBuckets(t *testing.T) {
	eat := NewBucketedInMemoryEAT()

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Same path, different buckets
	eat.Put(root1, "key", target1)
	eat.Put(root2, "key", target2)

	got1, err := eat.Get(root1, "key")
	if err != nil {
		t.Fatalf("Get root1 failed: %v", err)
	}

	got2, err := eat.Get(root2, "key")
	if err != nil {
		t.Fatalf("Get root2 failed: %v", err)
	}

	if got1.Equals(got2) {
		t.Error("different buckets should have independent values")
	}

	if !got1.Equals(target1) {
		t.Error("root1 should have target1")
	}

	if !got2.Equals(target2) {
		t.Error("root2 should have target2")
	}
}

func TestBucketedInMemoryEATPutBatch(t *testing.T) {
	eat := NewBucketedInMemoryEAT()

	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// PutBatch with multiple arcs
	arcs := map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	}

	err := eat.PutBatch(root, arcs)
	if err != nil {
		t.Fatalf("PutBatch failed: %v", err)
	}

	// Verify all arcs were stored
	if got, _ := eat.Get(root, "a"); !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}
	if got, _ := eat.Get(root, "b"); !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}
	if got, _ := eat.Get(root, "c"); !got.Equals(target3) {
		t.Error("wrong value for 'c'")
	}

	// Verify Len via View
	view := eat.View(root)
	if view.Len() != 3 {
		t.Errorf("expected Len 3, got %d", view.Len())
	}
}

func TestBucketedInMemoryEATPutBatchEmpty(t *testing.T) {
	eat := NewBucketedInMemoryEAT()
	root := newTestCID([]byte("root"))

	// Empty batch should succeed
	err := eat.PutBatch(root, map[string]cid.Cid{})
	if err != nil {
		t.Fatalf("PutBatch with empty map should succeed: %v", err)
	}
}

func TestBucketedInMemoryEATPutBatchOverwrite(t *testing.T) {
	eat := NewBucketedInMemoryEAT()

	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// First batch
	eat.PutBatch(root, map[string]cid.Cid{
		"a": target1,
		"b": target1,
	})

	// Second batch with updates
	eat.PutBatch(root, map[string]cid.Cid{
		"a": target2, // overwrite
		"c": target2, // new
	})

	// Verify
	if got, _ := eat.Get(root, "a"); !got.Equals(target2) {
		t.Error("'a' should be updated to target2")
	}
	if got, _ := eat.Get(root, "b"); !got.Equals(target1) {
		t.Error("'b' should remain target1")
	}
	if got, _ := eat.Get(root, "c"); !got.Equals(target2) {
		t.Error("'c' should be target2")
	}

	view := eat.View(root)
	if view.Len() != 3 {
		t.Errorf("expected Len 3, got %d", view.Len())
	}
}