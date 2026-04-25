package graph

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/kvstore/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func newTestCID(data []byte) cid.Cid {
	h, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, h)
}

func newTestStore() *Store {
	return NewStore(memory.New())
}

func newTestManager() *Manager {
	return NewManager(newTestStore())
}

func TestStoreCreateAndGet(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()

	g := &GraphMeta{
		ID:           "test-graph",
		Root:         newTestCID([]byte("root1")),
		State:        StateActive,
		Backend:      "kzg",
		ArcTableType: "overwrite",
	}

	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	got, err := s.Get(ctx, "test-graph")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if got.ID != g.ID {
		t.Errorf("expected ID %q, got %q", g.ID, got.ID)
	}
	if got.State != StateActive {
		t.Errorf("expected state %q, got %q", StateActive, got.State)
	}
}

func TestStoreCreateDuplicate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()

	g := &GraphMeta{ID: "dup", State: StateActive}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	if err := s.Create(ctx, g); err != ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()

	_, err := s.Get(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()

	g := &GraphMeta{ID: "to-delete", State: StateActive}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := s.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	got, err := s.Get(ctx, "to-delete")
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if !got.IsDeleted() {
		t.Error("expected graph to be deleted")
	}

	// Second delete should fail
	if err := s.Delete(ctx, "to-delete"); err != ErrDeleted {
		t.Errorf("expected ErrDeleted, got %v", err)
	}
}

func TestStoreList(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()

	s.Create(ctx, &GraphMeta{ID: "g1", State: StateActive})
	s.Create(ctx, &GraphMeta{ID: "g2", State: StateFrozen})
	s.Create(ctx, &GraphMeta{ID: "g3", State: StateDeleted})

	graphs, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	// Should only return active and frozen graphs
	if len(graphs) != 2 {
		t.Errorf("expected 2 graphs, got %d", len(graphs))
	}
}

func TestStoreUpdate(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()

	root := newTestCID([]byte("root1"))
	g := &GraphMeta{ID: "update-test", Root: root, State: StateActive, ArcCount: 1}
	if err := s.Create(ctx, g); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	newRoot := newTestCID([]byte("root2"))
	g.UpdateRoot(newRoot, 5)

	if err := s.Update(ctx, g); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	got, err := s.Get(ctx, "update-test")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Root.Equals(newRoot) {
		t.Error("root was not updated")
	}
	if got.ArcCount != 5 {
		t.Errorf("expected ArcCount=5, got %d", got.ArcCount)
	}
}

func TestManagerCreateGraph(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	g, err := m.CreateGraph(ctx, "my-graph", "kzg", "overwrite")
	if err != nil {
		t.Fatalf("CreateGraph failed: %v", err)
	}

	if g.ID != "my-graph" {
		t.Errorf("expected ID my-graph, got %q", g.ID)
	}
	if g.State != StateActive {
		t.Errorf("expected StateActive, got %q", g.State)
	}
	if g.ArcCount != 0 {
		t.Errorf("expected ArcCount=0, got %d", g.ArcCount)
	}
}

func TestManagerCreateDuplicate(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	_, err := m.CreateGraph(ctx, "dup", "kzg", "overwrite")
	if err != nil {
		t.Fatalf("first CreateGraph failed: %v", err)
	}

	_, err = m.CreateGraph(ctx, "dup", "kzg", "overwrite")
	if err != ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestManagerGetGraph(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "g1", "kzg", "overwrite")

	g, err := m.GetGraph(ctx, "g1")
	if err != nil {
		t.Fatalf("GetGraph failed: %v", err)
	}
	if g.ID != "g1" {
		t.Errorf("expected g1, got %q", g.ID)
	}

	// Test not found
	_, err = m.GetGraph(ctx, "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestManagerGetDeletedGraph(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "to-del", "kzg", "overwrite")
	m.DeleteGraph(ctx, "to-del")

	_, err := m.GetGraph(ctx, "to-del")
	if err != ErrDeleted {
		t.Errorf("expected ErrDeleted, got %v", err)
	}
}

func TestManagerUpdateGraph(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "up", "kzg", "overwrite")

	root := newTestCID([]byte("root1"))
	g, err := m.UpdateGraph(ctx, "up", root, 3)
	if err != nil {
		t.Fatalf("UpdateGraph failed: %v", err)
	}
	if !g.Root.Equals(root) {
		t.Error("root not updated")
	}
	if g.ArcCount != 3 {
		t.Errorf("expected ArcCount=3, got %d", g.ArcCount)
	}
}

func TestManagerUpdateFrozenGraph(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "frozen", "kzg", "overwrite")
	m.FreezeGraph(ctx, "frozen")

	root := newTestCID([]byte("root1"))
	_, err := m.UpdateGraph(ctx, "frozen", root, 1)
	if err != ErrFrozen {
		t.Errorf("expected ErrFrozen, got %v", err)
	}
}

func TestManagerFreezeGraph(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "freeze-me", "kzg", "overwrite")

	if err := m.FreezeGraph(ctx, "freeze-me"); err != nil {
		t.Fatalf("FreezeGraph failed: %v", err)
	}

	g, err := m.GetGraph(ctx, "freeze-me")
	if err != nil {
		t.Fatalf("GetGraph failed: %v", err)
	}
	if !g.IsFrozen() {
		t.Error("expected graph to be frozen")
	}
}

func TestManagerFreezeNonActiveGraph(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "f1", "kzg", "overwrite")
	m.FreezeGraph(ctx, "f1")

	// Cannot freeze already-frozen graph
	err := m.FreezeGraph(ctx, "f1")
	if err == nil {
		t.Error("expected error freezing already-frozen graph")
	}
}

func TestManagerListGraphs(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "a1", "kzg", "overwrite")
	m.CreateGraph(ctx, "a2", "kzg", "overwrite")
	m.CreateGraph(ctx, "d1", "kzg", "overwrite")
	m.DeleteGraph(ctx, "d1")

	graphs, err := m.ListGraphs(ctx)
	if err != nil {
		t.Fatalf("ListGraphs failed: %v", err)
	}

	if len(graphs) != 2 {
		t.Errorf("expected 2 graphs, got %d", len(graphs))
	}
}

func TestManagerRequireActive(t *testing.T) {
	ctx := context.Background()
	m := newTestManager()

	m.CreateGraph(ctx, "active", "kzg", "overwrite")
	m.CreateGraph(ctx, "frozen", "kzg", "overwrite")
	m.FreezeGraph(ctx, "frozen")

	_, err := m.RequireActive(ctx, "active")
	if err != nil {
		t.Errorf("expected active graph to pass, got %v", err)
	}

	_, err = m.RequireActive(ctx, "frozen")
	if err == nil {
		t.Error("expected error for frozen graph")
	}
}

func TestGraphStateTransitions(t *testing.T) {
	g := &GraphMeta{ID: "state-test", State: StateActive}

	// Active -> Frozen
	g.State = StateFrozen
	if !g.IsFrozen() {
		t.Error("expected frozen after state change")
	}

	// Frozen -> Deleted
	g.State = StateDeleted
	if !g.IsDeleted() {
		t.Error("expected deleted after state change")
	}
}

func TestGraphUpdateRoot(t *testing.T) {
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	g := &GraphMeta{ID: "update-root", Root: root1, ArcCount: 1}

	g.UpdateRoot(root2, 5)

	if !g.Root.Equals(root2) {
		t.Error("root not updated")
	}
	if g.ArcCount != 5 {
		t.Errorf("expected ArcCount=5, got %d", g.ArcCount)
	}
}
