package lineage

import (
	"context"
	"fmt"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func newMemoryStore() *Store {
	return NewStore(newMemoryKV())
}

// memoryKV is a simple in-memory KV store for testing.
type memoryKV struct {
	data map[string][]byte
}

func newMemoryKV() *memoryKV {
	return &memoryKV{data: make(map[string][]byte)}
}

func (m *memoryKV) Get(key string) ([]byte, bool) {
	v, ok := m.data[key]
	return v, ok
}

func (m *memoryKV) Set(key string, value []byte) error {
	m.data[key] = value
	return nil
}

func (m *memoryKV) Delete(key string) error {
	delete(m.data, key)
	return nil
}

func (m *memoryKV) Keys() []string {
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys
}

// ===== Store Tests =====

func TestStore_RecordAndGet(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()
	root := fakeCID("root1")

	rec := &LineageRecord{
		Root:  root,
		Depth: 1,
	}
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	got, err := s.Get(ctx, root)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Root.Equals(root) {
		t.Errorf("expected root %s, got %s", root, got.Root)
	}
}

func TestStore_RecordWithParent(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()
	root := fakeCID("root1")
	parent := fakeCID("parent1")

	rec := &LineageRecord{
		Root:   root,
		Parent: parent,
		Depth:  2,
	}
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	children, err := s.getChildren(parent.String())
	if err != nil {
		t.Fatalf("getChildren failed: %v", err)
	}
	if len(children) != 1 || !children[0].Equals(root) {
		t.Errorf("expected child %s, got %v", root, children)
	}
}

func TestStore_RecordDuplicate(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()
	root := fakeCID("root1")

	rec := &LineageRecord{Root: root, Depth: 1}
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("first Record failed: %v", err)
	}

	// Recording again should overwrite
	rec2 := &LineageRecord{Root: root, Depth: 5}
	if err := s.Record(ctx, rec2); err != nil {
		t.Fatalf("second Record failed: %v", err)
	}

	got, err := s.Get(ctx, root)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Depth != 5 {
		t.Errorf("expected depth 5, got %d", got.Depth)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()
	root := fakeCID("nonexistent")

	_, err := s.Get(ctx, root)
	if err == nil {
		t.Error("expected error for nonexistent root")
	}
}

func TestStore_Delete(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()
	root := fakeCID("root1")
	parent := fakeCID("parent1")

	// Record with parent
	rec := &LineageRecord{Root: root, Parent: parent, Depth: 2}
	if err := s.Record(ctx, rec); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// Delete
	if err := s.Delete(ctx, root); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify gone
	_, err := s.Get(ctx, root)
	if err == nil {
		t.Error("expected error after delete")
	}

	// Verify parent's children index is cleaned up
	children, _ := s.getChildren(parent.String())
	if len(children) != 0 {
		t.Errorf("expected 0 children, got %d", len(children))
	}
}

func TestStore_List(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()

	roots := make([]cid.Cid, 3)
	for i := 0; i < 3; i++ {
		roots[i] = fakeCID(fmt.Sprintf("root%d", i))
		rec := &LineageRecord{Root: roots[i], Depth: i + 1}
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record failed: %v", err)
		}
	}

	records, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d", len(records))
	}
}

func TestStore_Count(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()

	if s.Count(ctx) != 0 {
		t.Error("expected 0 records")
	}

	for i := 0; i < 5; i++ {
		root := fakeCID(fmt.Sprintf("root%d", i))
		rec := &LineageRecord{Root: root, Depth: i + 1}
		if err := s.Record(ctx, rec); err != nil {
			t.Fatalf("Record failed: %v", err)
		}
	}

	if s.Count(ctx) != 5 {
		t.Errorf("expected 5 records, got %d", s.Count(ctx))
	}
}

func TestStore_Ancestors(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()

	// Build chain: root -> v1 -> v2 -> v3 (oldest)
	v3 := fakeCID("v3")
	v2 := fakeCID("v2")
	v1 := fakeCID("v1")
	root := fakeCID("root")

	if err := s.Record(ctx, &LineageRecord{Root: v3, Depth: 1}); err != nil {
		t.Fatalf("Record v3: %v", err)
	}
	if err := s.Record(ctx, &LineageRecord{Root: v2, Parent: v3, Depth: 2}); err != nil {
		t.Fatalf("Record v2: %v", err)
	}
	if err := s.Record(ctx, &LineageRecord{Root: v1, Parent: v2, Depth: 3}); err != nil {
		t.Fatalf("Record v1: %v", err)
	}
	if err := s.Record(ctx, &LineageRecord{Root: root, Parent: v1, Depth: 4}); err != nil {
		t.Fatalf("Record root: %v", err)
	}

	ancestors, err := s.Ancestors(ctx, root, 0)
	if err != nil {
		t.Fatalf("Ancestors failed: %v", err)
	}
	if len(ancestors) != 3 {
		t.Errorf("expected 3 ancestors, got %d", len(ancestors))
	}
	if !ancestors[0].Equals(v1) || !ancestors[1].Equals(v2) || !ancestors[2].Equals(v3) {
		t.Errorf("ancestor order wrong: got %v", ancestors)
	}

	// With maxDepth
	ancestors, err = s.Ancestors(ctx, root, 2)
	if err != nil {
		t.Fatalf("Ancestors with maxDepth failed: %v", err)
	}
	if len(ancestors) != 2 {
		t.Errorf("expected 2 ancestors with maxDepth=2, got %d", len(ancestors))
	}
}

func TestStore_Descendants(t *testing.T) {
	s := newMemoryStore()
	ctx := context.Background()

	parent := fakeCID("parent")
	child1 := fakeCID("child1")
	child2 := fakeCID("child2")

	if err := s.Record(ctx, &LineageRecord{Root: child1, Parent: parent, Depth: 2}); err != nil {
		t.Fatalf("Record child1: %v", err)
	}
	if err := s.Record(ctx, &LineageRecord{Root: child2, Parent: parent, Depth: 2}); err != nil {
		t.Fatalf("Record child2: %v", err)
	}

	children, err := s.Descendants(ctx, parent)
	if err != nil {
		t.Fatalf("Descendants failed: %v", err)
	}
	if len(children) != 2 {
		t.Errorf("expected 2 children, got %d", len(children))
	}
}

// ===== Manager Tests =====

func TestManager_RecordAndGet(t *testing.T) {
	m := NewManager(newMemoryStore())
	ctx := context.Background()
	root := fakeCID("root1")
	parent := fakeCID("parent1")

	// Record parent first
	if err := m.Record(ctx, parent, cid.Undef, 3); err != nil {
		t.Fatalf("Record parent failed: %v", err)
	}

	if err := m.Record(ctx, root, parent, 5); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	rec, err := m.Get(ctx, root)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !rec.Root.Equals(root) {
		t.Errorf("expected root %s, got %s", root, rec.Root)
	}
	if rec.ArcCount != 5 {
		t.Errorf("expected arcCount 5, got %d", rec.ArcCount)
	}
	if rec.Depth != 2 {
		t.Errorf("expected depth 2, got %d", rec.Depth)
	}
}

func TestManager_ChainedRecord(t *testing.T) {
	m := NewManager(newMemoryStore())
	ctx := context.Background()

	v1 := fakeCID("v1")
	v2 := fakeCID("v2")
	v3 := fakeCID("v3")

	if err := m.Record(ctx, v1, cid.Undef, 10); err != nil {
		t.Fatalf("Record v1: %v", err)
	}
	if err := m.Record(ctx, v2, v1, 12); err != nil {
		t.Fatalf("Record v2: %v", err)
	}
	if err := m.Record(ctx, v3, v2, 15); err != nil {
		t.Fatalf("Record v3: %v", err)
	}

	d3, err := m.Depth(ctx, v3)
	if err != nil {
		t.Fatalf("Depth failed: %v", err)
	}
	if d3 != 3 {
		t.Errorf("expected depth 3, got %d", d3)
	}

	ancestors, err := m.Ancestors(ctx, v3, 0)
	if err != nil {
		t.Fatalf("Ancestors failed: %v", err)
	}
	if len(ancestors) != 2 {
		t.Errorf("expected 2 ancestors, got %d", len(ancestors))
	}
}

func TestManager_List(t *testing.T) {
	m := NewManager(newMemoryStore())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		root := fakeCID(fmt.Sprintf("root%d", i))
		if err := m.Record(ctx, root, cid.Undef, i*5); err != nil {
			t.Fatalf("Record failed: %v", err)
		}
	}

	records, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records, got %d", len(records))
	}
}

// ===== COW Tests =====

func TestManagerCOW_RecordWithShortcut(t *testing.T) {
	s := newMemoryStore()
	m := NewManagerCOW(s)
	ctx := context.Background()

	// Build a short chain
	v1 := fakeCID("v1")
	v2 := fakeCID("v2")
	v3 := fakeCID("v3")
	v4 := fakeCID("v4")

	if err := m.Record(ctx, v1, cid.Undef, 10); err != nil {
		t.Fatalf("Record v1: %v", err)
	}
	if err := m.Record(ctx, v2, v1, 12); err != nil {
		t.Fatalf("Record v2: %v", err)
	}
	if err := m.Record(ctx, v3, v2, 15); err != nil {
		t.Fatalf("Record v3: %v", err)
	}

	// Create a shortcut for v3 -> v1 (dist 2)
	if err := s.RecordShortcut(ctx, v3, v1, 2); err != nil {
		t.Fatalf("RecordShortcut: %v", err)
	}

	// Now record v4 with parent v3
	if err := m.RecordWithShortcut(ctx, v4, v3, 18); err != nil {
		t.Fatalf("RecordWithShortcut: %v", err)
	}

	// v4 should have a shortcut to v1 with dist 3
	shortcut, err := s.GetShortcut(ctx, v4)
	if err != nil {
		t.Fatalf("GetShortcut: %v", err)
	}
	if shortcut == nil {
		t.Fatal("expected shortcut for v4")
	}
	if !shortcut.To.Equals(v1) {
		t.Errorf("expected shortcut to v1, got %s", shortcut.To)
	}
	if shortcut.Dist != 3 {
		t.Errorf("expected dist 3, got %d", shortcut.Dist)
	}
}

func TestManagerCOW_GetAncestorFast(t *testing.T) {
	s := newMemoryStore()
	m := NewManagerCOW(s)
	ctx := context.Background()

	// Build chain: v4 -> v3 -> v2 -> v1 (oldest)
	v1 := fakeCID("v1")
	v2 := fakeCID("v2")
	v3 := fakeCID("v3")
	v4 := fakeCID("v4")

	if err := m.Record(ctx, v1, cid.Undef, 10); err != nil {
		t.Fatalf("Record v1: %v", err)
	}
	if err := m.Record(ctx, v2, v1, 12); err != nil {
		t.Fatalf("Record v2: %v", err)
	}
	if err := m.Record(ctx, v3, v2, 15); err != nil {
		t.Fatalf("Record v3: %v", err)
	}
	if err := m.Record(ctx, v4, v3, 18); err != nil {
		t.Fatalf("Record v4: %v", err)
	}

	// Create shortcut v4 -> v1 (dist 3)
	if err := s.RecordShortcut(ctx, v4, v1, 3); err != nil {
		t.Fatalf("RecordShortcut: %v", err)
	}

	ancestors, err := m.GetAncestorFast(ctx, v4, 0)
	if err != nil {
		t.Fatalf("GetAncestorFast: %v", err)
	}
	if len(ancestors) != 1 {
		t.Fatalf("expected 1 ancestor (the shortcut target), got %d", len(ancestors))
	}
	if !ancestors[0].Equals(v1) {
		t.Errorf("expected v1, got %s", ancestors[0])
	}
}

func TestManagerCOW_GetAncestorFast_Fallback(t *testing.T) {
	s := newMemoryStore()
	m := NewManagerCOW(s)
	ctx := context.Background()

	// Build chain without shortcuts
	v1 := fakeCID("v1")
	v2 := fakeCID("v2")
	v3 := fakeCID("v3")

	if err := m.Record(ctx, v1, cid.Undef, 10); err != nil {
		t.Fatalf("Record v1: %v", err)
	}
	if err := m.Record(ctx, v2, v1, 12); err != nil {
		t.Fatalf("Record v2: %v", err)
	}
	if err := m.Record(ctx, v3, v2, 15); err != nil {
		t.Fatalf("Record v3: %v", err)
	}

	// No shortcuts - should fall back to linear traversal
	ancestors, err := m.GetAncestorFast(ctx, v3, 0)
	if err != nil {
		t.Fatalf("GetAncestorFast: %v", err)
	}
	if len(ancestors) != 2 {
		t.Errorf("expected 2 ancestors, got %d", len(ancestors))
	}
	if !ancestors[0].Equals(v2) || !ancestors[1].Equals(v1) {
		t.Errorf("ancestor order wrong: got %v", ancestors)
	}
}

func TestLineageRecord_JSONRoundTrip(t *testing.T) {
	root := fakeCID("root1")
	parent := fakeCID("parent1")

	rec := &LineageRecord{
		Root:     root,
		Parent:   parent,
		Depth:    3,
		ArcCount: 15,
	}

	data, err := rec.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var got LineageRecord
	if err := got.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	if !got.Root.Equals(root) {
		t.Errorf("root: expected %s, got %s", root, got.Root)
	}
	if !got.Parent.Equals(parent) {
		t.Errorf("parent: expected %s, got %s", parent, got.Parent)
	}
	if got.Depth != 3 {
		t.Errorf("depth: expected 3, got %d", got.Depth)
	}
}
