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
	eat, err := NewEAT(kv, "test-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}
	if eat.GraphId() != "test-graph" {
		t.Error("wrong graphId")
	}

	// Nil KVStore
	_, err = NewEAT(nil, "test")
	if err == nil {
		t.Error("expected error for nil KVStore")
	}

	// Empty graphId
	_, err = NewEAT(kv, "")
	if err == nil {
		t.Error("expected error for empty graphId")
	}
}

func TestVersionedEATUpdate(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "versioned-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create first version (no parent)
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = eat.Update(root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify current
	if !eat.Current().Equals(root1) {
		t.Error("current should be root1")
	}

	// Get at root1
	got, err := eat.Get(root1, "a")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, err = eat.Get(root1, "b")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}
}

func TestVersionedEATVersionChain(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "chain-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

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
	err = eat.Update(root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update v1 failed: %v", err)
	}

	// Version 2: a -> target3 (override), b unchanged
	arcs2 := map[string]cid.Cid{
		"a": target3,
	}
	err = eat.Update(root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update v2 failed: %v", err)
	}

	// Version 3: c -> target3 (new), a and b unchanged
	arcs3 := map[string]cid.Cid{
		"c": target3,
	}
	err = eat.Update(root3, root2, arcs3)
	if err != nil {
		t.Fatalf("Update v3 failed: %v", err)
	}

	// Test resolution at root3

	// a should resolve to target3 (overridden at v2)
	got, err := eat.Get(root3, "a")
	if err != nil {
		t.Fatalf("Get a at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root3 should be target3")
	}

	// b should resolve to target2 (from v1)
	got, err = eat.Get(root3, "b")
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	// c should resolve to target3 (new at v3)
	got, err = eat.Get(root3, "c")
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("c at root3 should be target3")
	}

	// Test resolution at root2

	// a at root2 should be target3
	got, err = eat.Get(root2, "a")
	if err != nil {
		t.Fatalf("Get a at root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root2 should be target3")
	}

	// b at root2 should be target2
	got, err = eat.Get(root2, "b")
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// c at root2 should not exist
	_, err = eat.Get(root2, "c")
	if err == nil {
		t.Error("c at root2 should not exist")
	}

	// Test resolution at root1

	got, err = eat.Get(root1, "a")
	if err != nil {
		t.Fatalf("Get a at root1 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("a at root1 should be target1")
	}
}

func TestVersionedEATGetParent(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "parent-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	arcs1 := map[string]cid.Cid{
		"a": newTestCID([]byte("target1")),
	}
	eat.Update(root1, cid.Undef, arcs1)

	arcs2 := map[string]cid.Cid{
		"b": newTestCID([]byte("target2")),
	}
	eat.Update(root2, root1, arcs2)

	// GetParent
	parent, err := eat.GetParent(root2)
	if err != nil {
		t.Fatalf("GetParent failed: %v", err)
	}
	if !parent.Equals(root1) {
		t.Error("parent of root2 should be root1")
	}

	// First version has no parent
	parent, err = eat.GetParent(root1)
	if err != nil {
		t.Fatalf("GetParent root1 failed: %v", err)
	}
	if parent != cid.Undef {
		t.Error("root1 should have no parent")
	}
}

func TestVersionedEATGetLatest(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "latest-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))

	arcs1 := map[string]cid.Cid{
		"a": target1,
	}
	eat.Update(root1, cid.Undef, arcs1)

	// GetLatest
	got, err := eat.GetLatest("a")
	if err != nil {
		t.Fatalf("GetLatest failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value from GetLatest")
	}

	// Nonexistent
	_, err = eat.GetLatest("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

func TestVersionedEATView(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "view-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	arcs1 := map[string]cid.Cid{
		"a": target1,
	}
	eat.Update(root1, cid.Undef, arcs1)

	arcs2 := map[string]cid.Cid{
		"b": target2,
	}
	eat.Update(root2, root1, arcs2)

	// View at root2
	view := eat.View(root2)

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

func TestVersionedEATIterate(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "iter-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	eat.Update(root1, cid.Undef, arcs1)

	// Iterate at root1 (direct arcs only)
	iter := eat.Iterate(root1)

	count := 0
	for {
		path, c, ok := iter.Next()
		if !ok {
			break
		}
		count++
		switch path {
		case "a":
			if !c.Equals(target1) {
				t.Error("wrong value for 'a'")
			}
		case "b":
			if !c.Equals(target2) {
				t.Error("wrong value for 'b'")
			}
		default:
			t.Errorf("unexpected path: %s", path)
		}
	}

	if count != 2 {
		t.Errorf("expected 2 arcs, got %d", count)
	}

	// Len at root1
	if eat.Len(root1) != 2 {
		t.Errorf("expected Len 2, got %d", eat.Len(root1))
	}
}

func TestVersionedEATFlattenedIterator(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "flat-iter-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	eat.Update(root1, cid.Undef, arcs1)

	arcs2 := map[string]cid.Cid{
		"c": target3,
	}
	eat.Update(root2, root1, arcs2)

	// Flattened view at root2
	view := eat.View(root2)
	iter := view.Iterate()

	seen := make(map[string]bool)
	for {
		path, c, ok := iter.Next()
		if !ok {
			break
		}
		seen[path] = true

		switch path {
		case "a":
			if !c.Equals(target1) {
				t.Error("wrong value for 'a'")
			}
		case "b":
			if !c.Equals(target2) {
				t.Error("wrong value for 'b'")
			}
		case "c":
			if !c.Equals(target3) {
				t.Error("wrong value for 'c'")
			}
		default:
			t.Errorf("unexpected path: %s", path)
		}
	}

	// Should see all 3 arcs
	if len(seen) != 3 {
		t.Errorf("expected 3 arcs, got %d", len(seen))
	}

	for _, p := range []string{"a", "b", "c"} {
		if !seen[p] {
			t.Errorf("path %s not seen", p)
		}
	}
}

func TestVersionedEATMultipleGraphs(t *testing.T) {
	kv := memory.New() // Shared KVStore

	eat1, err := NewEAT(kv, "graph1")
	if err != nil {
		t.Fatalf("NewEAT graph1 failed: %v", err)
	}

	eat2, err := NewEAT(kv, "graph2")
	if err != nil {
		t.Fatalf("NewEAT graph2 failed: %v", err)
	}

	root1a := newTestCID([]byte("root1a"))
	root2a := newTestCID([]byte("root2a"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create versions in different graphs
	eat1.Update(root1a, cid.Undef, map[string]cid.Cid{"a": target1})
	eat2.Update(root2a, cid.Undef, map[string]cid.Cid{"a": target2})

	// Should be independent
	got1, _ := eat1.Get(root1a, "a")
	got2, _ := eat2.Get(root2a, "a")

	if got1.Equals(got2) {
		t.Error("different graphs should have independent values")
	}

	if !got1.Equals(target1) {
		t.Error("graph1 should have target1")
	}

	if !got2.Equals(target2) {
		t.Error("graph2 should have target2")
	}
}

func TestVersionedEATCopyOnWrite(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "cow-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

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
	eat.Update(root1, cid.Undef, arcs1)

	// v2: a modified, use COW to copy b
	arcs2 := map[string]cid.Cid{
		"a": target3,
	}
	eat.Update(root2, root1, arcs2)

	// Apply COW: copy b from ancestors to root2
	modifiedPaths := map[string]bool{"a": true}
	err = eat.CopyOnWrite(root2, root1, modifiedPaths)
	if err != nil {
		t.Fatalf("CopyOnWrite failed: %v", err)
	}

	// v3: new arc c
	arcs3 := map[string]cid.Cid{
		"c": target3,
	}
	eat.Update(root3, root2, arcs3)

	// Apply COW: copy a and b to root3
	modifiedPaths3 := map[string]bool{"c": true}
	err = eat.CopyOnWrite(root3, root2, modifiedPaths3)
	if err != nil {
		t.Fatalf("CopyOnWrite v3 failed: %v", err)
	}

	// Now Len at root3 should include all copied arcs
	len3 := eat.Len(root3)
	if len3 < 3 {
		t.Errorf("expected at least 3 arcs after COW, got %d", len3)
	}

	// Resolution should still work
	got, err := eat.Get(root3, "a")
	if err != nil {
		t.Fatalf("Get a failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a should be target3")
	}

	got, err = eat.Get(root3, "b")
	if err != nil {
		t.Fatalf("Get b failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b should be target2")
	}

	got, err = eat.Get(root3, "c")
	if err != nil {
		t.Fatalf("Get c failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("c should be target3")
	}
}

func TestVersionedEATDeleteViaUpdate(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "delete-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

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
	eat.Update(root1, cid.Undef, arcs1)

	// v2: delete 'a' using cid.Undef (tombstone)
	arcs2 := map[string]cid.Cid{
		"a": cid.Undef, // tombstone - marks 'a' as deleted
	}
	eat.Update(root2, root1, arcs2)

	// At root2, 'a' should not be found (tombstone stops the search)
	_, err = eat.Get(root2, "a")
	if err == nil {
		t.Error("'a' should be deleted at root2")
	}

	// 'b' should still be accessible (from root1)
	got, err := eat.Get(root2, "b")
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// At root1, 'a' should still exist (tombstone is at root2, not root1)
	got, err = eat.Get(root1, "a")
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
	eat.Update(root3, root2, arcs3)

	// At root3, 'a' should still not be found
	_, err = eat.Get(root3, "a")
	if err == nil {
		t.Error("'a' should be deleted at root3")
	}

	// 'b' and 'c' should work
	got, err = eat.Get(root3, "b")
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	got, err = eat.Get(root3, "c")
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("c at root3 should be target1")
	}
}