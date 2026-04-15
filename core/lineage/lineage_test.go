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

func newMemoryManager() *Manager {
	return NewManager(newMemoryKV())
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

// ===== Manager Tests =====

func TestManager_RecordAndGet(t *testing.T) {
	m := newMemoryManager()
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

func TestManager_RecordWithParent(t *testing.T) {
	m := newMemoryManager()
	ctx := context.Background()
	root := fakeCID("root1")
	parent := fakeCID("parent1")

	if err := m.Record(ctx, root, parent, 5); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	children, err := m.getChildren(parent.String())
	if err != nil {
		t.Fatalf("getChildren failed: %v", err)
	}
	if len(children) != 1 || !children[0].Equals(root) {
		t.Errorf("expected child %s, got %v", root, children)
	}
}

func TestManager_RecordDuplicate(t *testing.T) {
	m := newMemoryManager()
	ctx := context.Background()
	root := fakeCID("root1")

	if err := m.Record(ctx, root, cid.Undef, 1); err != nil {
		t.Fatalf("first Record failed: %v", err)
	}

	// Recording again should overwrite
	if err := m.Record(ctx, root, cid.Undef, 5); err != nil {
		t.Fatalf("second Record failed: %v", err)
	}

	got, err := m.Get(ctx, root)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Depth != 1 {
		t.Errorf("expected depth 1, got %d", got.Depth)
	}
}

func TestManager_GetNotFound(t *testing.T) {
	m := newMemoryManager()
	ctx := context.Background()
	root := fakeCID("nonexistent")

	_, err := m.Get(ctx, root)
	if err == nil {
		t.Error("expected error for nonexistent root")
	}
}

func TestManager_Delete(t *testing.T) {
	m := newMemoryManager()
	ctx := context.Background()
	root := fakeCID("root1")
	parent := fakeCID("parent1")

	// Record with parent
	if err := m.Record(ctx, root, parent, 5); err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	// Delete
	if err := m.Delete(ctx, root); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify gone
	_, err := m.Get(ctx, root)
	if err == nil {
		t.Error("expected error after delete")
	}

	// Verify parent's children index is cleaned up
	children, _ := m.getChildren(parent.String())
	if len(children) != 0 {
		t.Errorf("expected 0 children, got %d", len(children))
	}
}

func TestManager_List(t *testing.T) {
	m := newMemoryManager()
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

func TestManager_Count(t *testing.T) {
	m := newMemoryManager()
	ctx := context.Background()

	if m.Count(ctx) != 0 {
		t.Error("expected 0 records")
	}

	for i := 0; i < 5; i++ {
		root := fakeCID(fmt.Sprintf("root%d", i))
		if err := m.Record(ctx, root, cid.Undef, i+1); err != nil {
			t.Fatalf("Record failed: %v", err)
		}
	}

	if m.Count(ctx) != 5 {
		t.Errorf("expected 5 records, got %d", m.Count(ctx))
	}
}

func TestManager_Ancestors(t *testing.T) {
	m := newMemoryManager()
	ctx := context.Background()

	// Build chain: root -> v1 -> v2 -> v3 (oldest)
	v3 := fakeCID("v3")
	v2 := fakeCID("v2")
	v1 := fakeCID("v1")
	root := fakeCID("root")

	if err := m.Record(ctx, v3, cid.Undef, 1); err != nil {
		t.Fatalf("Record v3: %v", err)
	}
	if err := m.Record(ctx, v2, v3, 1); err != nil {
		t.Fatalf("Record v2: %v", err)
	}
	if err := m.Record(ctx, v1, v2, 1); err != nil {
		t.Fatalf("Record v1: %v", err)
	}
	if err := m.Record(ctx, root, v1, 1); err != nil {
		t.Fatalf("Record root: %v", err)
	}

	ancestors, err := m.Ancestors(ctx, root, 0)
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
	ancestors, err = m.Ancestors(ctx, root, 2)
	if err != nil {
		t.Fatalf("Ancestors with maxDepth failed: %v", err)
	}
	if len(ancestors) != 2 {
		t.Errorf("expected 2 ancestors with maxDepth=2, got %d", len(ancestors))
	}
}

func TestManager_Descendants(t *testing.T) {
	m := newMemoryManager()
	ctx := context.Background()

	parent := fakeCID("parent")
	child1 := fakeCID("child1")
	child2 := fakeCID("child2")

	if err := m.Record(ctx, child1, parent, 5); err != nil {
		t.Fatalf("Record child1: %v", err)
	}
	if err := m.Record(ctx, child2, parent, 5); err != nil {
		t.Fatalf("Record child2: %v", err)
	}

	children, err := m.Descendants(ctx, parent)
	if err != nil {
		t.Fatalf("Descendants failed: %v", err)
	}
	if len(children) != 2 {
		t.Errorf("expected 2 children, got %d", len(children))
	}
}

func TestManager_ChainedRecord(t *testing.T) {
	m := newMemoryManager()
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
