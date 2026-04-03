package memory

import (
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/types/kvstore/memory"
	cid "github.com/ipfs/go-cid"
)

// === EAT Tests ===

func TestEATNew(t *testing.T) {
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

func TestEATUpdateAndGet(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "mygraph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// First update (no old root)
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = eat.Update(root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root1
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

	// Update with new root (overwrites 'a', adds 'c')
	target3 := newTestCID([]byte("target3"))
	arcs2 := map[string]cid.Cid{
		"a": target3, // overwrite
		"c": target3, // new
	}
	err = eat.Update(root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get via root2 should work
	got, err = eat.Get(root2, "a")
	if err != nil {
		t.Fatalf("Get via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'a' via root2")
	}

	got, err = eat.Get(root2, "b")
	if err != nil {
		t.Fatalf("Get b via root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("'b' should still be target2")
	}

	got, err = eat.Get(root2, "c")
	if err != nil {
		t.Fatalf("Get c via root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("wrong value for 'c' via root2")
	}

	// Old root1 should no longer work
	_, err = eat.Get(root1, "a")
	if err == nil {
		t.Error("old root should no longer work after update")
	}
}

func TestEATDelete(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "delete-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Setup
	eat.Update(root1, cid.Undef, map[string]cid.Cid{
		"a": target,
		"b": target,
	})

	// Delete 'a'
	err = eat.Delete(root2, root1, "a")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// 'a' should be gone
	_, err = eat.Get(root2, "a")
	if err == nil {
		t.Error("'a' should be deleted")
	}

	// 'b' should still exist
	got, err := eat.Get(root2, "b")
	if err != nil {
		t.Fatalf("Get b failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("'b' should still exist")
	}
}

func TestEATView(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "view-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	eat.Update(root, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
	})

	view := eat.View(root)

	got, ok := view.Get("a")
	if !ok {
		t.Error("expected to find 'a'")
	}
	if !got.Equals(target1) {
		t.Error("wrong value from view")
	}

	if view.Len() != 2 {
		t.Errorf("expected Len 2, got %d", view.Len())
	}

	// View with invalid root should return empty view
	invalidRoot := newTestCID([]byte("invalid"))
	emptyView := eat.View(invalidRoot)
	if emptyView.Len() != 0 {
		t.Error("invalid root should return empty view")
	}
}

func TestEATIterate(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "iter-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root := newTestCID([]byte("root"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	eat.Update(root, cid.Undef, map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	})

	iter := eat.Iterate()

	count := 0
	seen := make(map[string]bool)
	for {
		path, c, ok := iter.Next()
		if !ok {
			break
		}
		count++
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

	if iter.Err() != nil {
		t.Errorf("iterator error: %v", iter.Err())
	}

	if count != 3 {
		t.Errorf("expected 3 arcs, got %d", count)
	}
}

func TestEATMultipleGraphs(t *testing.T) {
	kv := memory.New() // Shared KVStore

	eat1, err := NewEAT(kv, "graph1")
	if err != nil {
		t.Fatalf("NewEAT graph1 failed: %v", err)
	}

	eat2, err := NewEAT(kv, "graph2")
	if err != nil {
		t.Fatalf("NewEAT graph2 failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Same path, different graphs
	eat1.Update(root1, cid.Undef, map[string]cid.Cid{"key": target1})
	eat2.Update(root2, cid.Undef, map[string]cid.Cid{"key": target2})

	// Should be independent
	got1, _ := eat1.Get(root1, "key")
	got2, _ := eat2.Get(root2, "key")

	if got1.Equals(got2) {
		t.Error("different graphs should have independent values")
	}

	if !got1.Equals(target1) {
		t.Error("graph1 should have target1")
	}

	if !got2.Equals(target2) {
		t.Error("graph2 should have target2")
	}

	// Len should be per-graph
	if eat1.Len() != 1 {
		t.Error("graph1.Len should be 1")
	}
	if eat2.Len() != 1 {
		t.Error("graph2.Len should be 1")
	}
}

func TestEATClear(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "clear-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))
	eat.Update(root, cid.Undef, map[string]cid.Cid{
		"a": target,
		"b": target,
	})

	err = eat.Clear()
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	if eat.Len() != 0 {
		t.Error("expected empty after clear")
	}
}

func TestEATBatchUpdate(t *testing.T) {
	kv := memory.New()
	eat, err := NewEAT(kv, "batch-graph")
	if err != nil {
		t.Fatalf("NewEAT failed: %v", err)
	}

	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	// Large batch
	arcs1 := make(map[string]cid.Cid)
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("arc%d", i)
		arcs1[path] = newTestCID([]byte(path))
	}

	err = eat.Update(root1, cid.Undef, arcs1)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if eat.Len() != 100 {
		t.Errorf("expected 100 arcs, got %d", eat.Len())
	}

	// Second batch (partial overwrite)
	arcs2 := make(map[string]cid.Cid)
	for i := 50; i < 150; i++ {
		path := fmt.Sprintf("arc%d", i)
		arcs2[path] = newTestCID([]byte("new_" + path))
	}

	err = eat.Update(root2, root1, arcs2)
	if err != nil {
		t.Fatalf("Update 2 failed: %v", err)
	}

	// Should have 150 arcs (0-149)
	if eat.Len() != 150 {
		t.Errorf("expected 150 arcs after second update, got %d", eat.Len())
	}

	// Verify old root doesn't work
	_, err = eat.Get(root1, "arc0")
	if err == nil {
		t.Error("old root should not work")
	}

	// Verify new root works
	got, err := eat.Get(root2, "arc0")
	if err != nil {
		t.Fatalf("Get arc0 via root2 failed: %v", err)
	}
	// arc0 should still have original value (not in arcs2)
	if !got.Equals(arcs1["arc0"]) {
		t.Error("arc0 should have original value")
	}

	// arc50 should have new value
	got, err = eat.Get(root2, "arc50")
	if err != nil {
		t.Fatalf("Get arc50 via root2 failed: %v", err)
	}
	if !got.Equals(arcs2["arc50"]) {
		t.Error("arc50 should have new value")
	}
}