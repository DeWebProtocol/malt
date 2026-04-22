package writer

import (
	"context"
	"sync"
	"testing"

	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Test helpers.

func newTestWriter(t *testing.T) (*Writer, *overwrite.EAT, mapping.Semantics, *kvg) {
	t.Helper()

	// Memory KVStore
	kv := kvmemory.New()

	// Overwrite EAT
	e, err := overwrite.NewEAT(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("failed to create EAT: %v", err)
	}

	// KZG commitment scheme
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("failed to create KZG scheme: %v", err)
	}

	semantic, err := mappingradix.NewMap(scheme, e)
	if err != nil {
		t.Fatalf("failed to create mapping semantic: %v", err)
	}

	// Writer (no lineage recorder for basic tests)
	w := NewWriter(semantic, e, nil)

	return w, e, semantic, kv
}

type kvg = kvmemory.KV

func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func makeArcSet(pairs map[string]cid.Cid) *arcset.Set {
	out := make(map[string]cid.Cid, len(pairs)+1)
	for path, target := range pairs {
		out[path] = target
	}
	if _, ok := out["@payload"]; !ok {
		out["@payload"] = fakeCID("payload")
	}
	return arcset.NewSetFrom(out)
}

// mockLineageRecorder is a thread-safe mock for testing lineage recording.
type mockLineageRecorder struct {
	mu      sync.Mutex
	records []struct{ newRoot, oldRoot cid.Cid }
}

func (m *mockLineageRecorder) Record(_ context.Context, _ string, newRoot, oldRoot cid.Cid) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, struct{ newRoot, oldRoot cid.Cid }{newRoot, oldRoot})
	return nil
}

func (m *mockLineageRecorder) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

func (m *mockLineageRecorder) Last() (cid.Cid, cid.Cid) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.records) == 0 {
		return cid.Undef, cid.Undef
	}
	last := m.records[len(m.records)-1]
	return last.newRoot, last.oldRoot
}

// Tests.

func TestWriter_UpdateArc_Insert(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	// Create initial structure
	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
	})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}
	if !root.Defined() {
		t.Fatal("root not defined after CreateStructure")
	}

	// Insert a new arc
	newTarget := fakeCID("data-c")
	result, err := w.UpdateArc(ctx, bucketId, root, "c", newTarget)
	if err != nil {
		t.Fatalf("UpdateArc insert failed: %v", err)
	}

	if result.Op != ArcInsert {
		t.Errorf("expected ArcInsert, got %v", result.Op)
	}
	if !result.NewRoot.Defined() {
		t.Error("newRoot not defined")
	}
	if result.NewRoot.Equals(root) {
		t.Error("newRoot should differ from oldRoot after insert")
	}
	if result.NewTarget != newTarget {
		t.Errorf("newTarget mismatch: expected %s, got %s", newTarget, result.NewTarget)
	}

	// Verify the new arc is accessible
	target, err := w.GetArc(ctx, bucketId, result.NewRoot, "c")
	if err != nil {
		t.Fatalf("GetArc failed after insert: %v", err)
	}
	if target != newTarget {
		t.Errorf("getArc returned wrong target: expected %s, got %s", newTarget, target)
	}

	// Verify old arcs are still accessible
	for path, expected := range map[string]cid.Cid{"a": fakeCID("data-a"), "b": fakeCID("data-b")} {
		got, err := w.GetArc(ctx, bucketId, result.NewRoot, path)
		if err != nil {
			t.Fatalf("GetArc failed for %s: %v", path, err)
		}
		if got != expected {
			t.Errorf("GetArc(%s): expected %s, got %s", path, expected, got)
		}
	}
}

func TestWriter_UpdateArc_Replace(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
	})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Replace arc "a"
	newTarget := fakeCID("data-a-new")
	result, err := w.UpdateArc(ctx, bucketId, root, "a", newTarget)
	if err != nil {
		t.Fatalf("UpdateArc replace failed: %v", err)
	}

	if result.Op != ArcReplace {
		t.Errorf("expected ArcReplace, got %v", result.Op)
	}
	if result.OldTarget != fakeCID("data-a") {
		t.Errorf("oldTarget wrong: expected %s, got %s", fakeCID("data-a"), result.OldTarget)
	}

	// Verify replacement
	got, err := w.GetArc(ctx, bucketId, result.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc failed: %v", err)
	}
	if got != newTarget {
		t.Errorf("replaced arc value wrong: expected %s, got %s", newTarget, got)
	}
}

func TestWriter_UpdateArc_Delete(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
	})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Delete arc "a"
	result, err := w.UpdateArc(ctx, bucketId, root, "a", cid.Undef)
	if err != nil {
		t.Fatalf("UpdateArc delete failed: %v", err)
	}

	if result.Op != ArcDelete {
		t.Errorf("expected ArcDelete, got %v", result.Op)
	}

	// Verify deleted
	_, err = w.GetArc(ctx, bucketId, result.NewRoot, "a")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}

	// Arc "b" should still be accessible
	got, err := w.GetArc(ctx, bucketId, result.NewRoot, "b")
	if err != nil {
		t.Fatalf("GetArc for 'b' failed: %v", err)
	}
	if got != fakeCID("data-b") {
		t.Errorf("arc 'b' changed after delete of 'a': got %s", got)
	}
}

func TestWriter_UpdateArc_InvalidInputs(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	// Undefined root
	_, err := w.UpdateArc(ctx, bucketId, cid.Undef, "a", fakeCID("data"))
	if err != ErrInvalidRoot {
		t.Errorf("expected ErrInvalidRoot, got %v", err)
	}

	// Empty path
	arcs := makeArcSet(map[string]cid.Cid{"a": fakeCID("data-a")})
	root, _ := w.CreateStructure(ctx, bucketId, arcs)
	_, err = w.UpdateArc(ctx, bucketId, root, "", fakeCID("data"))
	if err != ErrEmptyPath {
		t.Errorf("expected ErrEmptyPath, got %v", err)
	}
}

func TestWriter_BatchUpdateArcs(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
		"c": fakeCID("data-c"),
	})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Batch update: replace "a", insert "d", delete "c"
	updates := map[string]cid.Cid{
		"a": fakeCID("data-a-new"),
		"d": fakeCID("data-d"),
		"c": cid.Undef,
	}

	result, err := w.BatchUpdateArcs(ctx, bucketId, root, updates)
	if err != nil {
		t.Fatalf("BatchUpdateArcs failed: %v", err)
	}

	if !result.NewRoot.Defined() {
		t.Error("newRoot not defined")
	}
	if result.NewRoot.Equals(root) {
		t.Error("newRoot should differ after batch update")
	}

	// Verify per-arc results
	if result.PerArc["a"].Op != ArcReplace {
		t.Errorf("expected ArcReplace for 'a', got %v", result.PerArc["a"].Op)
	}
	if result.PerArc["d"].Op != ArcInsert {
		t.Errorf("expected ArcInsert for 'd', got %v", result.PerArc["d"].Op)
	}
	if result.PerArc["c"].Op != ArcDelete {
		t.Errorf("expected ArcDelete for 'c', got %v", result.PerArc["c"].Op)
	}

	// Verify final state
	checks := map[string]struct {
		expected cid.Cid
		exists   bool
	}{
		"a": {fakeCID("data-a-new"), true},
		"b": {fakeCID("data-b"), true},
		"d": {fakeCID("data-d"), true},
		"c": {cid.Undef, false},
	}

	for path, check := range checks {
		got, err := w.GetArc(ctx, bucketId, result.NewRoot, path)
		if check.exists {
			if err != nil {
				t.Fatalf("GetArc(%s) failed: %v", path, err)
			}
			if got != check.expected {
				t.Errorf("GetArc(%s): expected %s, got %s", path, check.expected, got)
			}
		} else {
			if err == nil {
				t.Errorf("expected GetArc(%s) to fail, got %s", path, got)
			}
		}
	}
}

func TestWriter_BatchUpdateArcs_InvalidInputs(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	// Undefined root
	_, err := w.BatchUpdateArcs(ctx, bucketId, cid.Undef, map[string]cid.Cid{"a": fakeCID("data")})
	if err != ErrInvalidRoot {
		t.Errorf("expected ErrInvalidRoot, got %v", err)
	}

	// Empty updates
	arcs := makeArcSet(map[string]cid.Cid{"a": fakeCID("data-a")})
	root, _ := w.CreateStructure(ctx, bucketId, arcs)
	_, err = w.BatchUpdateArcs(ctx, bucketId, root, map[string]cid.Cid{})
	if err == nil {
		t.Error("expected error for empty updates, got nil")
	}
}

func TestWriter_CreateStructure(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	// Create structure with arcs
	arcs := makeArcSet(map[string]cid.Cid{
		"foo": fakeCID("data-foo"),
		"bar": fakeCID("data-bar"),
	})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}
	if !root.Defined() {
		t.Fatal("root not defined")
	}

	// Verify arcs are accessible
	for path, expected := range map[string]cid.Cid{
		"foo": fakeCID("data-foo"),
		"bar": fakeCID("data-bar"),
	} {
		got, err := w.GetArc(ctx, bucketId, root, path)
		if err != nil {
			t.Fatalf("GetArc(%s) failed: %v", path, err)
		}
		if got != expected {
			t.Errorf("GetArc(%s): expected %s, got %s", path, expected, got)
		}
	}
}

func TestWriter_CanonicalizesPathsAtWriteBoundary(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"/foo//bar/": fakeCID("data-foo-bar"),
	})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	got, err := w.GetArc(ctx, bucketId, root, "foo/bar")
	if err != nil {
		t.Fatalf("GetArc failed: %v", err)
	}
	if got != fakeCID("data-foo-bar") {
		t.Errorf("GetArc(foo/bar): expected %s, got %s", fakeCID("data-foo-bar"), got)
	}

	updated, err := w.UpdateArc(ctx, bucketId, root, "/foo//bar/", fakeCID("data-foo-bar-v2"))
	if err != nil {
		t.Fatalf("UpdateArc failed: %v", err)
	}
	got, err = w.GetArc(ctx, bucketId, updated.NewRoot, "/foo//bar/")
	if err != nil {
		t.Fatalf("GetArc after update failed: %v", err)
	}
	if got != fakeCID("data-foo-bar-v2") {
		t.Errorf("GetArc after update: expected %s, got %s", fakeCID("data-foo-bar-v2"), got)
	}
}

func TestWriter_CreateStructure_NilArcSet(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	_, err := w.CreateStructure(ctx, bucketId, nil)
	if err == nil {
		t.Error("expected error for nil arc set, got nil")
	}
}

func TestWriter_GetArc_NotFound(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	arcs := makeArcSet(map[string]cid.Cid{"a": fakeCID("data-a")})
	root, _ := w.CreateStructure(ctx, bucketId, arcs)

	_, err := w.GetArc(ctx, bucketId, root, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent arc, got nil")
	}
}

func TestWriter_GetSnapshot(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"x": fakeCID("data-x"),
		"y": fakeCID("data-y"),
	})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	snapshot, err := w.GetSnapshot(ctx, bucketId, root)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if snapshot.Len() != 3 {
		t.Errorf("expected 3 arcs including @payload, got %d", snapshot.Len())
	}

	target, ok := snapshot.Get(arcset.CanonicalizePath("x"))
	if !ok {
		t.Fatal("arc 'x' not found in snapshot")
	}
	if target != fakeCID("data-x") {
		t.Errorf("snapshot arc 'x' wrong: got %s", target)
	}
}

func TestWriter_LineageRecorder(t *testing.T) {
	// Memory KVStore
	kv := kvmemory.New()
	e, _ := overwrite.NewEAT(overwrite.WithKVStore(kv))
	scheme, _ := kzg.NewScheme()
	semantic, _ := mappingradix.NewMap(scheme, e)
	rec := &mockLineageRecorder{}
	w := NewWriter(semantic, e, rec)

	ctx := context.Background()
	bucketId := "test"

	// Create structure (should record lineage: root → cid.Undef)
	arcs := makeArcSet(map[string]cid.Cid{"a": fakeCID("data-a")})
	root, err := w.CreateStructure(ctx, bucketId, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	if rec.Count() != 1 {
		t.Errorf("expected 1 lineage record after CreateStructure, got %d", rec.Count())
	}

	// Update (should record lineage: newRoot → root)
	newTarget := fakeCID("data-b")
	result, err := w.UpdateArc(ctx, bucketId, root, "b", newTarget)
	if err != nil {
		t.Fatalf("UpdateArc failed: %v", err)
	}

	if rec.Count() != 2 {
		t.Errorf("expected 2 lineage records after UpdateArc, got %d", rec.Count())
	}

	lastNew, lastOld := rec.Last()
	if !lastNew.Equals(result.NewRoot) {
		t.Errorf("lineage newRoot mismatch")
	}
	if !lastOld.Equals(root) {
		t.Errorf("lineage oldRoot mismatch")
	}
}

func TestWriter_UpdateArc_UpdateThenGet(t *testing.T) {
	// Verify that after multiple updates, the latest structure root
	// reflects all accumulated changes.
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	bucketId := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"alpha": fakeCID("v0-alpha"),
	})
	root, _ := w.CreateStructure(ctx, bucketId, arcs)

	// Update 1: insert "beta"
	r1, err := w.UpdateArc(ctx, bucketId, root, "beta", fakeCID("v0-beta"))
	if err != nil {
		t.Fatalf("Update 1 (insert beta) failed: %v", err)
	}

	// Update 2: replace "alpha"
	r2, err := w.UpdateArc(ctx, bucketId, r1.NewRoot, "alpha", fakeCID("v1-alpha"))
	if err != nil {
		t.Fatalf("Update 2 (replace alpha) failed: %v", err)
	}

	// Update 3: insert "gamma"
	r3, err := w.UpdateArc(ctx, bucketId, r2.NewRoot, "gamma", fakeCID("v0-gamma"))
	if err != nil {
		t.Fatalf("Update 3 (insert gamma) failed: %v", err)
	}

	// Verify final state from r3.NewRoot
	finalRoot := r3.NewRoot
	checks := map[string]cid.Cid{
		"alpha": fakeCID("v1-alpha"),
		"beta":  fakeCID("v0-beta"),
		"gamma": fakeCID("v0-gamma"),
	}
	for path, expected := range checks {
		got, err := w.GetArc(ctx, bucketId, finalRoot, path)
		if err != nil {
			t.Fatalf("GetArc(%s) after chain of updates: %v", path, err)
		}
		if got != expected {
			t.Errorf("GetArc(%s) after updates: expected %s, got %s", path, expected, got)
		}
	}
}
