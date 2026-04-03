package versioned

import (
	"testing"

	"github.com/dewebprotocol/malt/core/types/kvstore/memory"
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

// === Versioned EAT Tests ===

func TestVersionedEATNew(t *testing.T) {
	kv := memory.New()

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

func TestVersionedEATUpdate(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "versioned-graph"
	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create first version (no parent)
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = eat.Update(bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify current
	if !eat.Current(bucketId).Equals(root1) {
		t.Error("current should be root1")
	}

	// Get at root1
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
}

func TestVersionedEATVersionChain(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

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
	err = eat.Update(bucketId, root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update v1 failed: %v", err)
	}

	// Version 2: a -> target3 (override), b unchanged
	arcs2 := map[string]cid.Cid{
		"a": target3,
	}
	err = eat.Update(bucketId, root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update v2 failed: %v", err)
	}

	// Version 3: c -> target3 (new), a and b unchanged
	arcs3 := map[string]cid.Cid{
		"c": target3,
	}
	err = eat.Update(bucketId, root3, root2, arcs3)
	if err != nil {
		t.Fatalf("Update v3 failed: %v", err)
	}

	// Test resolution at root3

	// a should resolve to target3 (overridden at v2)
	got, err := eat.Get(bucketId, root3, "a")
	if err != nil {
		t.Fatalf("Get a at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root3 should be target3")
	}

	// b should resolve to target2 (from v1)
	got, err = eat.Get(bucketId, root3, "b")
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	// c should resolve to target3 (new at v3)
	got, err = eat.Get(bucketId, root3, "c")
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("c at root3 should be target3")
	}

	// Test resolution at root2

	// a at root2 should be target3
	got, err = eat.Get(bucketId, root2, "a")
	if err != nil {
		t.Fatalf("Get a at root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root2 should be target3")
	}

	// b at root2 should be target2
	got, err = eat.Get(bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// c at root2 should not exist
	_, err = eat.Get(bucketId, root2, "c")
	if err == nil {
		t.Error("c at root2 should not exist")
	}

	// Test resolution at root1

	got, err = eat.Get(bucketId, root1, "a")
	if err != nil {
		t.Fatalf("Get a at root1 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("a at root1 should be target1")
	}
}

func TestVersionedEATGetParent(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "parent-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	arcs1 := map[string]cid.Cid{
		"a": newTestCID([]byte("target1")),
	}
	eat.Update(bucketId, root1, cid.Undef, arcs1)

	arcs2 := map[string]cid.Cid{
		"b": newTestCID([]byte("target2")),
	}
	eat.Update(bucketId, root2, root1, arcs2)

	// GetParent
	parent, err := eat.GetParent(bucketId, root2)
	if err != nil {
		t.Fatalf("GetParent failed: %v", err)
	}
	if !parent.Equals(root1) {
		t.Error("parent of root2 should be root1")
	}

	// First version has no parent
	parent, err = eat.GetParent(bucketId, root1)
	if err != nil {
		t.Fatalf("GetParent root1 failed: %v", err)
	}
	if parent != cid.Undef {
		t.Error("root1 should have no parent")
	}
}

func TestVersionedEATView(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "view-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	arcs1 := map[string]cid.Cid{
		"a": target1,
	}
	eat.Update(bucketId, root1, cid.Undef, arcs1)

	arcs2 := map[string]cid.Cid{
		"b": target2,
	}
	eat.Update(bucketId, root2, root1, arcs2)

	// View at root2
	view := eat.View(bucketId, root2)

	got, ok := view.Get("a")
	if !ok {
		t.Error("expected to find 'a' at root2 view")
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, ok = view.Get("b")
	if !ok {
		t.Error("expected to find 'b' at root2 view")
	}
	if !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}

	// TotalLen
	if view.Len() != 2 {
		t.Errorf("expected Len 2, got %d", view.Len())
	}
}

func TestVersionedEATDeleteViaUpdate(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

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
	eat.Update(bucketId, root1, cid.Undef, arcs1)

	// v2: delete 'a' using cid.Undef (tombstone)
	arcs2 := map[string]cid.Cid{
		"a": cid.Undef, // tombstone - marks 'a' as deleted
	}
	eat.Update(bucketId, root2, root1, arcs2)

	// At root2, 'a' should not be found (tombstone stops the search)
	_, err = eat.Get(bucketId, root2, "a")
	if err == nil {
		t.Error("'a' should be deleted at root2")
	}

	// 'b' should still be accessible (from root1)
	got, err := eat.Get(bucketId, root2, "b")
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// At root1, 'a' should still exist (tombstone is at root2, not root1)
	got, err = eat.Get(bucketId, root1, "a")
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
	eat.Update(bucketId, root3, root2, arcs3)

	// At root3, 'a' should still not be found
	_, err = eat.Get(bucketId, root3, "a")
	if err == nil {
		t.Error("'a' should be deleted at root3")
	}

	// 'b' and 'c' should work
	got, err = eat.Get(bucketId, root3, "b")
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	got, err = eat.Get(bucketId, root3, "c")
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("c at root3 should be target1")
	}
}

func TestVersionedEATMultipleBuckets(t *testing.T) {
	kv := memory.New() // Shared KVStore

	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1a := newTestCID([]byte("root1a"))
	root2a := newTestCID([]byte("root2a"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create versions in different buckets
	eat.Update("bucket1", root1a, cid.Undef, map[string]cid.Cid{"a": target1})
	eat.Update("bucket2", root2a, cid.Undef, map[string]cid.Cid{"a": target2})

	// Should be independent
	got1, _ := eat.Get("bucket1", root1a, "a")
	got2, _ := eat.Get("bucket2", root2a, "a")

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

func TestVersionedEATCopyOnWrite(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv)
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	bucketId := "cow-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// v1: a, b
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	eat.Update(bucketId, root1, cid.Undef, arcs1)

	// v2: a modified, use COW to copy b
	arcs2 := map[string]cid.Cid{
		"a": target3,
	}
	eat.Update(bucketId, root2, root1, arcs2)

	// Apply COW: copy b from ancestors to root2
	modifiedPaths := map[string]bool{"a": true}
	err = eat.CopyOnWrite(bucketId, root2, root1, modifiedPaths)
	if err != nil {
		t.Fatalf("CopyOnWrite failed: %v", err)
	}

	// v3: new arc c
	arcs3 := map[string]cid.Cid{
		"c": target3,
	}
	eat.Update(bucketId, root3, root2, arcs3)

	// Apply COW: copy a and b to root3
	modifiedPaths3 := map[string]bool{"c": true}
	err = eat.CopyOnWrite(bucketId, root3, root2, modifiedPaths3)
	if err != nil {
		t.Fatalf("CopyOnWrite v3 failed: %v", err)
	}

	// Now Len at root3 should include all copied arcs
	len3 := eat.Len(bucketId, root3)
	if len3 < 3 {
		t.Errorf("expected at least 3 arcs after COW, got %d", len3)
	}

	// Resolution should still work
	got, err := eat.Get(bucketId, root3, "a")
	if err != nil {
		t.Fatalf("Get a failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a should be target3")
	}

	got, err = eat.Get(bucketId, root3, "b")
	if err != nil {
		t.Fatalf("Get b failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b should be target2")
	}

	got, err = eat.Get(bucketId, root3, "c")
	if err != nil {
		t.Fatalf("Get c failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("c should be target3")
	}
}