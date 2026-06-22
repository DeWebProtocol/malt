package writer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	semanticmapping "github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/runtime/arctable/overwrite"
	"github.com/dewebprotocol/malt/runtime/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// failingArcTable wraps an ArcTable and fails Update calls while the fail flag
// is set. It is the test seam for the cross-layer atomicity gap: the semantic
// layer commits a valid newRoot, but the index write fails.
//
// The semantic runtime (radix) persists its own node/bucket slots through
// ArcTable.Update using cid.Undef as the newRoot (see radix storeNodeSlots /
// storeBucketEntries). The writer's logical arc write uses the real newRoot.
// To inject a failure at exactly the writer-level index write without breaking
// the semantic layer's internal persistence, we only fail updates whose
// newRoot is defined — i.e. the logical root-publishing writes.
type failingArcTable struct {
	inner arctable.ArcTable
	fail  bool
	calls int
}

func (f *failingArcTable) Get(ctx context.Context, namespace string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	return f.inner.Get(ctx, namespace, root, path)
}

func (f *failingArcTable) BatchGet(ctx context.Context, namespace string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	return f.inner.BatchGet(ctx, namespace, root, paths)
}

func (f *failingArcTable) Update(ctx context.Context, namespace string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error {
	f.calls++
	// Only fail the logical root-publishing write. The semantic runtime's
	// slot/bucket persistence (newRoot == cid.Undef) must succeed so that the
	// semantic layer can produce a valid newRoot in the first place.
	if f.fail && newRoot.Defined() {
		return errInjectedIndexFailure
	}
	return f.inner.Update(ctx, namespace, newRoot, oldRoot, arcs)
}

func (f *failingArcTable) Snapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error) {
	return f.inner.Snapshot(ctx, namespace, root)
}

func (f *failingArcTable) Iterate(ctx context.Context, namespace string, root cid.Cid) arcset.Iterator {
	return f.inner.Iterate(ctx, namespace, root)
}

func (f *failingArcTable) Close() error { return f.inner.Close() }

var errInjectedIndexFailure = errors.New("injected arctable failure")

// rootDeletingFailingArcTable simulates a non-atomic ArcTable backend that has
// already invalidated oldRoot before the logical root-publishing write fails.
// Retrying the original writer operation against oldRoot cannot recover from
// this state; the captured IndexDelta must be replayed instead.
type rootDeletingFailingArcTable struct {
	inner arctable.ArcTable
	kv    *kvg
	fail  bool
}

func (f *rootDeletingFailingArcTable) Get(ctx context.Context, namespace string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	return f.inner.Get(ctx, namespace, root, path)
}

func (f *rootDeletingFailingArcTable) BatchGet(ctx context.Context, namespace string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	return f.inner.BatchGet(ctx, namespace, root, paths)
}

func (f *rootDeletingFailingArcTable) Update(ctx context.Context, namespace string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error {
	if f.fail && newRoot.Defined() && oldRoot.Defined() {
		if err := f.kv.Delete(ctx, arctable.RootKeyFormat(oldRoot)); err != nil {
			return err
		}
		return errInjectedIndexFailure
	}
	return f.inner.Update(ctx, namespace, newRoot, oldRoot, arcs)
}

func (f *rootDeletingFailingArcTable) Snapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error) {
	return f.inner.Snapshot(ctx, namespace, root)
}

func (f *rootDeletingFailingArcTable) Iterate(ctx context.Context, namespace string, root cid.Cid) arcset.Iterator {
	return f.inner.Iterate(ctx, namespace, root)
}

func (f *rootDeletingFailingArcTable) Close() error { return f.inner.Close() }

// partialArcFailingArcTable simulates a non-atomic batch failure after one arc
// from a multi-arc delta has already been applied. That intermediate namespace
// state is neither IndexBase nor the full expected-after state, so
// RetryIndexWrite must fail closed: it cannot distinguish this partial write
// from a later successful subset write.
type partialArcFailingArcTable struct {
	inner arctable.ArcTable
	kv    *kvg
	fail  bool
}

func (f *partialArcFailingArcTable) Get(ctx context.Context, namespace string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	return f.inner.Get(ctx, namespace, root, path)
}

func (f *partialArcFailingArcTable) BatchGet(ctx context.Context, namespace string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	return f.inner.BatchGet(ctx, namespace, root, paths)
}

func (f *partialArcFailingArcTable) Update(ctx context.Context, namespace string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error {
	if f.fail && newRoot.Defined() && oldRoot.Defined() {
		arcMap, err := arcset.ToPathMap(arcs)
		if err != nil {
			return err
		}
		if err := f.kv.Delete(ctx, arctable.RootKeyFormat(oldRoot)); err != nil {
			return err
		}
		if err := f.kv.Put(ctx, arctable.RootKeyFormat(newRoot), []byte(namespace)); err != nil {
			return err
		}
		path := arcset.CanonicalizePath("a")
		target, ok := arcMap[path]
		if !ok || !target.Defined() {
			return errInjectedIndexFailure
		}
		if err := f.kv.Put(ctx, arctable.DefaultArcKey(namespace, path), target.Bytes()); err != nil {
			return err
		}
		return errInjectedIndexFailure
	}
	return f.inner.Update(ctx, namespace, newRoot, oldRoot, arcs)
}

func (f *partialArcFailingArcTable) Snapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error) {
	return f.inner.Snapshot(ctx, namespace, root)
}

func (f *partialArcFailingArcTable) Iterate(ctx context.Context, namespace string, root cid.Cid) arcset.Iterator {
	return f.inner.Iterate(ctx, namespace, root)
}

func (f *partialArcFailingArcTable) Close() error { return f.inner.Close() }

// newFailingTestWriter builds a writer whose ArcTable Update is controlled by
// the returned *failingArcTable. The semantic layer is real (radix over
// overwrite ArcTable), so it commits a cryptographically valid newRoot before
// the index write is attempted.
func newFailingTestWriter(t *testing.T) (*Writer, *failingArcTable) {
	t.Helper()
	kv := memory.New()
	e, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable: %v", err)
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme: %v", err)
	}
	// The semantic layer writes its radix node slots through the same ArcTable
	// instance, so wrap once and share it. Node-slot writes go through Update
	// too; we only fail the *logical* index write by toggling fail at the right
	// moment in each test.
	wrapped := &failingArcTable{inner: e}
	maps, err := radix.NewMap(scheme, wrapped)
	if err != nil {
		t.Fatalf("NewMap: %v", err)
	}
	return NewWriter(maps, wrapped), wrapped
}

func newRootDeletingFailureWriter(t *testing.T) (*Writer, *rootDeletingFailingArcTable) {
	t.Helper()
	kv := memory.New()
	e, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable: %v", err)
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme: %v", err)
	}
	wrapped := &rootDeletingFailingArcTable{inner: e, kv: kv}
	maps, err := radix.NewMap(scheme, wrapped)
	if err != nil {
		t.Fatalf("NewMap: %v", err)
	}
	return NewWriter(maps, wrapped), wrapped
}

func newPartialArcFailureWriter(t *testing.T) (*Writer, *partialArcFailingArcTable) {
	t.Helper()
	kv := memory.New()
	e, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable: %v", err)
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme: %v", err)
	}
	wrapped := &partialArcFailingArcTable{inner: e, kv: kv}
	maps, err := radix.NewMap(scheme, wrapped)
	if err != nil {
		t.Fatalf("NewMap: %v", err)
	}
	return NewWriter(maps, wrapped), wrapped
}

// TestUpdateArc_IndexWriteFailureReturnsNewRoot is the core regression guard
// for review finding #1: when the semantic layer produces a newRoot but the
// ArcTable index write fails, the returned error must carry newRoot so the
// caller can retry the idempotent index write. Previously the newRoot was
// discarded and the root became unreadable via the index.
func TestUpdateArc_IndexWriteFailureReturnsNewRoot(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-update-fail"
	w, failing := newFailingTestWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")
	newValueA := makeCIDLocal(t, "new-value-a")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	// Fail only the writer-level index Update. The semantic Update still
	// succeeds and produces a valid newRoot.
	failing.fail = true
	_, err = w.UpdateArc(ctx, namespace, root, "a", newValueA)
	failing.fail = false
	if err == nil {
		t.Fatal("UpdateArc should have failed when ArcTable.Update failed")
	}

	var idxErr *IndexWriteFailedError
	if !errors.As(err, &idxErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrIndexWriteFailed) {
		t.Errorf("errors.Is(err, ErrIndexWriteFailed) = false, want true")
	}
	if !errors.Is(err, errInjectedIndexFailure) {
		t.Errorf("errors.Is(err, errInjectedIndexFailure) = false; underlying cause lost")
	}
	if !idxErr.NewRoot.Defined() {
		t.Fatal("IndexWriteFailedError.NewRoot is undefined")
	}
	if idxErr.NewRoot.Equals(root) {
		t.Error("IndexWriteFailedError.NewRoot equals old root; semantic layer did not advance")
	}
	if idxErr.IndexDelta == nil {
		t.Fatal("IndexWriteFailedError.IndexDelta is nil")
	}
	if idxErr.IndexBase == nil {
		t.Fatal("IndexWriteFailedError.IndexBase is nil")
	}

	// The semantic root is valid but unreadable via the index before retry:
	// GetArc against newRoot must fail because the index write never landed.
	if _, err := w.GetArc(ctx, namespace, idxErr.NewRoot, "a"); err == nil {
		t.Error("GetArc(newRoot, a) succeeded before retry; index write should be missing")
	}

	// Recovery replays the captured ArcTable transition. This is stronger than
	// re-running the original writer operation because a non-atomic backend may
	// have partially invalidated oldRoot before returning the failure.
	if err := idxErr.RetryIndexWrite(ctx, failing); err != nil {
		t.Fatalf("RetryIndexWrite: %v", err)
	}
	got, err := w.GetArc(ctx, namespace, idxErr.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc after retry: %v", err)
	}
	if !got.Equals(newValueA) {
		t.Errorf("after retry a = %s, want %s", got, newValueA)
	}
}

// TestBatchUpdateArcs_IndexWriteFailureReturnsNewRoot mirrors the above for
// the batch path.
func TestBatchUpdateArcs_IndexWriteFailureReturnsNewRoot(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-batch-fail"
	w, failing := newFailingTestWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")
	valueB := makeCIDLocal(t, "value-b")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
		"b":        valueB,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	newA := makeCIDLocal(t, "new-a")
	newB := makeCIDLocal(t, "new-b")

	failing.fail = true
	_, err = w.BatchUpdateArcs(ctx, namespace, root, map[string]cid.Cid{
		"a": newA,
		"b": newB,
	})
	failing.fail = false
	if err == nil {
		t.Fatal("BatchUpdateArcs should have failed when ArcTable.Update failed")
	}

	var idxErr *IndexWriteFailedError
	if !errors.As(err, &idxErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}
	if !idxErr.NewRoot.Defined() || idxErr.NewRoot.Equals(root) {
		t.Fatalf("IndexWriteFailedError.NewRoot not advanced: %s", idxErr.NewRoot)
	}
	if idxErr.IndexDelta == nil {
		t.Fatal("IndexWriteFailedError.IndexDelta is nil")
	}
	if idxErr.IndexBase == nil {
		t.Fatal("IndexWriteFailedError.IndexBase is nil")
	}

	if err := idxErr.RetryIndexWrite(ctx, failing); err != nil {
		t.Fatalf("RetryIndexWrite: %v", err)
	}
	for path, want := range map[string]cid.Cid{"a": newA, "b": newB} {
		got, err := w.GetArc(ctx, namespace, idxErr.NewRoot, path)
		if err != nil {
			t.Fatalf("GetArc(%s) after retry: %v", path, err)
		}
		if !got.Equals(want) {
			t.Fatalf("GetArc(%s) = %s, want %s", path, got, want)
		}
	}
}

// TestApply_MapDeltaIndexWriteFailure covers the semantic-mutation Apply path
// (commitMapDelta) which has the same cross-layer window.
func TestApply_MapDeltaIndexWriteFailure(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-apply-fail"
	w, failing := newFailingTestWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	newA := makeCIDLocal(t, "new-a")
	mut := SemanticMutation{
		BaseRoot: root,
		Deltas: []ArcSetDelta{{
			Object: root,
			Kind:   arcset.KindMap,
			Changes: mustWriterDelta(t, arcset.KindMap, []arcset.ArcChange{
				{
					Coordinate: mustMapCoordinate(t, "a"),
					Before:     targetRefPtr(arcset.NewCASTarget(valueA)),
					After:      targetRefPtr(arcset.NewCASTarget(newA)),
				},
			}),
		}},
	}

	failing.fail = true
	_, err = w.Apply(ctx, namespace, mut)
	failing.fail = false
	if err == nil {
		t.Fatal("Apply should have failed when ArcTable.Update failed")
	}

	var idxErr *IndexWriteFailedError
	if !errors.As(err, &idxErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}
	if !idxErr.NewRoot.Defined() {
		t.Fatal("IndexWriteFailedError.NewRoot is undefined")
	}
	if idxErr.IndexDelta == nil {
		t.Fatal("IndexWriteFailedError.IndexDelta is nil")
	}
	if idxErr.IndexBase == nil {
		t.Fatal("IndexWriteFailedError.IndexBase is nil")
	}
}

// TestUpdateArc_IndexWriteRetrySurvivesMissingBaseRoot covers a non-atomic
// backend window where the old root mapping has already been removed before
// ArcTable.Update reports failure. In that state, re-running the original
// writer operation cannot recover because Snapshot(oldRoot) no longer finds the
// mandatory payload binding; replaying IndexDelta from the error still works.
func TestUpdateArc_IndexWriteRetrySurvivesMissingBaseRoot(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-partial-index-fail"
	w, failing := newRootDeletingFailureWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")
	newA := makeCIDLocal(t, "new-a")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	failing.fail = true
	_, err = w.UpdateArc(ctx, namespace, root, "a", newA)
	failing.fail = false
	if err == nil {
		t.Fatal("UpdateArc should have failed when ArcTable.Update partially failed")
	}
	var idxErr *IndexWriteFailedError
	if !errors.As(err, &idxErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}
	if idxErr.IndexDelta == nil {
		t.Fatal("IndexWriteFailedError.IndexDelta is nil")
	}
	if idxErr.IndexBase == nil {
		t.Fatal("IndexWriteFailedError.IndexBase is nil")
	}

	_, retryErr := w.UpdateArc(ctx, namespace, root, "a", newA)
	if !errors.Is(retryErr, ErrMissingPayloadBinding) {
		t.Fatalf("retrying original UpdateArc error = %v, want ErrMissingPayloadBinding", retryErr)
	}

	if err := idxErr.RetryIndexWrite(ctx, failing); err != nil {
		t.Fatalf("RetryIndexWrite: %v", err)
	}
	got, err := w.GetArc(ctx, namespace, idxErr.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc after RetryIndexWrite: %v", err)
	}
	if !got.Equals(newA) {
		t.Fatalf("a after RetryIndexWrite = %s, want %s", got, newA)
	}
}

// TestUpdateArc_IndexWriteRetryRejectsStaleReplay guards overwrite ArcTable's
// namespace-scoped physical arc keys. A stale failed delta must not be replayed
// after a later successful write advances the same namespace, otherwise the
// later root remains present but resolves to stale arc values.
func TestUpdateArc_IndexWriteRetryRejectsStaleReplay(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-stale-retry"
	w, failing := newFailingTestWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")
	staleA := makeCIDLocal(t, "stale-a")
	laterA := makeCIDLocal(t, "later-a")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	failing.fail = true
	_, err = w.UpdateArc(ctx, namespace, root, "a", staleA)
	failing.fail = false
	if err == nil {
		t.Fatal("first UpdateArc should have failed when ArcTable.Update failed")
	}
	var staleErr *IndexWriteFailedError
	if !errors.As(err, &staleErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}
	if staleErr.IndexBase == nil || staleErr.IndexDelta == nil {
		t.Fatalf("stale error missing retry material: base=%v delta=%v", staleErr.IndexBase, staleErr.IndexDelta)
	}

	later, err := w.UpdateArc(ctx, namespace, root, "a", laterA)
	if err != nil {
		t.Fatalf("later UpdateArc: %v", err)
	}
	got, err := w.GetArc(ctx, namespace, later.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc(laterRoot) before stale retry: %v", err)
	}
	if !got.Equals(laterA) {
		t.Fatalf("laterRoot before stale retry = %s, want %s", got, laterA)
	}

	err = staleErr.RetryIndexWrite(ctx, failing)
	if !errors.Is(err, ErrStaleRoot) {
		t.Fatalf("stale RetryIndexWrite error = %v, want ErrStaleRoot", err)
	}
	got, err = w.GetArc(ctx, namespace, later.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc(laterRoot) after stale retry: %v", err)
	}
	if !got.Equals(laterA) {
		t.Fatalf("stale RetryIndexWrite changed laterRoot a = %s, want %s", got, laterA)
	}
}

func TestWriterRetryIndexWriteMarksOldRootStale(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-writer-retry-freshness"
	w, failing := newFailingTestWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")
	retryA := makeCIDLocal(t, "retry-a")
	laterA := makeCIDLocal(t, "later-a")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	failing.fail = true
	_, err = w.UpdateArc(ctx, namespace, root, "a", retryA)
	failing.fail = false
	if err == nil {
		t.Fatal("UpdateArc should have failed when ArcTable.Update failed")
	}
	var idxErr *IndexWriteFailedError
	if !errors.As(err, &idxErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}

	if err := w.RetryIndexWrite(ctx, idxErr); err != nil {
		t.Fatalf("Writer.RetryIndexWrite: %v", err)
	}
	got, err := w.GetArc(ctx, namespace, idxErr.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc after Writer.RetryIndexWrite: %v", err)
	}
	if !got.Equals(retryA) {
		t.Fatalf("a after Writer.RetryIndexWrite = %s, want %s", got, retryA)
	}

	w2 := NewWriter(w.semantic, failing)
	_, err = w2.UpdateArc(ctx, namespace, root, "a", laterA)
	if !errors.Is(err, ErrStaleRoot) {
		t.Fatalf("second writer UpdateArc after retry error = %v, want ErrStaleRoot", err)
	}
}

// TestBatchUpdateArcs_IndexWriteRetryRejectsPartialDelta verifies that retry
// rejects partially applied multi-path deltas for overwrite-like backends. A
// subset of delta paths can be indistinguishable from a later successful subset
// write, so completing the stale batch would risk corrupting namespace-scoped
// arc values for that later root.
func TestBatchUpdateArcs_IndexWriteRetryRejectsPartialDelta(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-partial-delta"
	w, failing := newPartialArcFailureWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")
	valueB := makeCIDLocal(t, "value-b")
	newA := makeCIDLocal(t, "new-a")
	newB := makeCIDLocal(t, "new-b")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
		"b":        valueB,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	failing.fail = true
	_, err = w.BatchUpdateArcs(ctx, namespace, root, map[string]cid.Cid{
		"a": newA,
		"b": newB,
	})
	failing.fail = false
	if err == nil {
		t.Fatal("BatchUpdateArcs should have failed after partial arc apply")
	}
	var idxErr *IndexWriteFailedError
	if !errors.As(err, &idxErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}

	gotA, err := w.GetArc(ctx, namespace, idxErr.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc(a) after partial failure: %v", err)
	}
	if !gotA.Equals(newA) {
		t.Fatalf("partial failure a = %s, want %s", gotA, newA)
	}
	gotB, err := w.GetArc(ctx, namespace, idxErr.NewRoot, "b")
	if err != nil {
		t.Fatalf("GetArc(b) after partial failure: %v", err)
	}
	if !gotB.Equals(valueB) {
		t.Fatalf("partial failure b = %s, want old value %s", gotB, valueB)
	}

	if err := idxErr.RetryIndexWrite(ctx, failing); !errors.Is(err, ErrStaleRoot) {
		t.Fatalf("RetryIndexWrite after partial delta error = %v, want ErrStaleRoot", err)
	}
	gotB, err = w.GetArc(ctx, namespace, idxErr.NewRoot, "b")
	if err != nil {
		t.Fatalf("GetArc(b) after rejected RetryIndexWrite: %v", err)
	}
	if !gotB.Equals(valueB) {
		t.Fatalf("rejected RetryIndexWrite changed b = %s, want old value %s", gotB, valueB)
	}
}

// TestBatchUpdateArcs_IndexWriteRetryRejectsSubsetWriteStaleReplay covers a
// stale retry that looks like partial progress if each delta path is checked
// independently: the failed batch wants to update both a and b, then a later
// successful write updates only a to the same target. Retrying the failed batch
// must not publish b's stale target into the namespace-scoped overwrite table.
func TestBatchUpdateArcs_IndexWriteRetryRejectsSubsetWriteStaleReplay(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-stale-batch-subset"
	w, failing := newFailingTestWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")
	valueB := makeCIDLocal(t, "value-b")
	batchA := makeCIDLocal(t, "batch-a")
	batchB := makeCIDLocal(t, "batch-b")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
		"b":        valueB,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	failing.fail = true
	_, err = w.BatchUpdateArcs(ctx, namespace, root, map[string]cid.Cid{
		"a": batchA,
		"b": batchB,
	})
	failing.fail = false
	if err == nil {
		t.Fatal("BatchUpdateArcs should have failed when ArcTable.Update failed")
	}
	var staleErr *IndexWriteFailedError
	if !errors.As(err, &staleErr) {
		t.Fatalf("expected *IndexWriteFailedError, got %T: %v", err, err)
	}
	if staleErr.IndexBase == nil || staleErr.IndexDelta == nil {
		t.Fatalf("stale error missing retry material: base=%v delta=%v", staleErr.IndexBase, staleErr.IndexDelta)
	}

	later, err := w.UpdateArc(ctx, namespace, root, "a", batchA)
	if err != nil {
		t.Fatalf("later UpdateArc: %v", err)
	}
	gotA, err := w.GetArc(ctx, namespace, later.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc(laterRoot, a) before stale retry: %v", err)
	}
	gotB, err := w.GetArc(ctx, namespace, later.NewRoot, "b")
	if err != nil {
		t.Fatalf("GetArc(laterRoot, b) before stale retry: %v", err)
	}
	if !gotA.Equals(batchA) || !gotB.Equals(valueB) {
		t.Fatalf("laterRoot before stale retry = {a:%s b:%s}, want {a:%s b:%s}", gotA, gotB, batchA, valueB)
	}

	err = staleErr.RetryIndexWrite(ctx, failing)
	if !errors.Is(err, ErrStaleRoot) {
		t.Fatalf("stale batch RetryIndexWrite error = %v, want ErrStaleRoot", err)
	}
	gotA, err = w.GetArc(ctx, namespace, later.NewRoot, "a")
	if err != nil {
		t.Fatalf("GetArc(laterRoot, a) after stale retry: %v", err)
	}
	gotB, err = w.GetArc(ctx, namespace, later.NewRoot, "b")
	if err != nil {
		t.Fatalf("GetArc(laterRoot, b) after stale retry: %v", err)
	}
	if !gotA.Equals(batchA) || !gotB.Equals(valueB) {
		t.Fatalf("stale batch RetryIndexWrite changed laterRoot = {a:%s b:%s}, want {a:%s b:%s}", gotA, gotB, batchA, valueB)
	}
}

// TestUpdateArc_ClassificationStillCorrectFromSnapshot guards Fix #2: with
// oldTarget now derived from the snapshot instead of a separate Get, insert /
// replace / delete classification must remain correct, including the no-op
// (both undefined) case.
func TestUpdateArc_ClassificationStillCorrectFromSnapshot(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-classify"
	w, _ := newFailingTestWriter(t)

	payload := makeCIDLocal(t, "payload")
	valueA := makeCIDLocal(t, "value-a")

	root, err := w.CreateStructure(ctx, namespace, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}

	// Replace existing arc.
	r, err := w.UpdateArc(ctx, namespace, root, "a", makeCIDLocal(t, "replace"))
	if err != nil {
		t.Fatalf("replace UpdateArc: %v", err)
	}
	if r.Op != ArcReplace {
		t.Errorf("replace: Op = %s, want replace", r.Op)
	}

	// Insert new arc.
	r, err = w.UpdateArc(ctx, namespace, r.NewRoot, "b", makeCIDLocal(t, "inserted"))
	if err != nil {
		t.Fatalf("insert UpdateArc: %v", err)
	}
	if r.Op != ArcInsert {
		t.Errorf("insert: Op = %s, want insert", r.Op)
	}

	// Delete existing arc.
	r, err = w.UpdateArc(ctx, namespace, r.NewRoot, "b", cid.Undef)
	if err != nil {
		t.Fatalf("delete UpdateArc: %v", err)
	}
	if r.Op != ArcDelete {
		t.Errorf("delete: Op = %s, want delete", r.Op)
	}

	// No-op: deleting a path that does not exist must report a no-op with
	// ArcInsert op (matching pre-Fix behavior at writer.go:395-404).
	r, err = w.UpdateArc(ctx, namespace, r.NewRoot, "b", cid.Undef)
	if err != nil {
		t.Fatalf("no-op UpdateArc: %v", err)
	}
	if r.Op != ArcInsert || !r.NewRoot.Equals(r.OldRoot) {
		t.Errorf("no-op: Op = %s, NewRoot advanced; expected no-op", r.Op)
	}
}

// TestUpdateArc_CorruptStoredTargetFailsClosed guards the classification path
// against corrupt overwrite ArcTable entries. overwrite.Snapshot skips values
// that cannot be cid.Cast; UpdateArc must still use targeted Get for oldTarget
// classification so deleting a corrupt existing arc does not become a silent
// "both undefined" no-op.
func TestUpdateArc_CorruptStoredTargetFailsClosed(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-corrupt-update"
	w, _, _, kv := newTestWriter(t)

	root, err := w.CreateStructure(ctx, namespace, makeArcSet(map[string]cid.Cid{
		"a": fakeCID("value-a"),
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}
	corruptStoredArcValue(t, ctx, kv, namespace, "a")

	_, err = w.UpdateArc(ctx, namespace, root, "a", cid.Undef)
	if err == nil {
		t.Fatal("UpdateArc deleting corrupt stored target succeeded; want fail-closed error")
	}
	if !strings.Contains(err.Error(), "ArcTable.Get failed") {
		t.Fatalf("UpdateArc error = %v, want targeted ArcTable.Get failure", err)
	}
}

// TestBatchUpdateArcs_CorruptStoredTargetFailsClosed guards the batch path
// against corrupt overwrite ArcTable entries. BatchUpdateArcs classifies through
// BatchGet, so a corrupt stored CID may be omitted by the index lookup, but the
// semantic layer must still reject the mismatched old value instead of silently
// committing a partial view.
func TestBatchUpdateArcs_CorruptStoredTargetFailsClosed(t *testing.T) {
	ctx := context.Background()
	namespace := "ns-corrupt-batch"
	w, _, _, kv := newTestWriter(t)

	root, err := w.CreateStructure(ctx, namespace, makeArcSet(map[string]cid.Cid{
		"a": fakeCID("value-a"),
		"b": fakeCID("value-b"),
	}))
	if err != nil {
		t.Fatalf("CreateStructure: %v", err)
	}
	corruptStoredArcValue(t, ctx, kv, namespace, "b")

	_, err = w.BatchUpdateArcs(ctx, namespace, root, map[string]cid.Cid{
		"a": fakeCID("new-a"),
		"b": cid.Undef,
	})
	if err == nil {
		t.Fatal("BatchUpdateArcs with corrupt stored target succeeded; want fail-closed error")
	}
	if !strings.Contains(err.Error(), "semantic.BatchUpdate failed") {
		t.Fatalf("BatchUpdateArcs error = %v, want semantic old-value mismatch failure", err)
	}
}

func makeCIDLocal(t *testing.T, data string) cid.Cid {
	t.Helper()
	mhash, err := mh.Sum([]byte(data), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("mh.Sum: %v", err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}

func corruptStoredArcValue(t *testing.T, ctx context.Context, kv *kvg, namespace, path string) {
	t.Helper()
	key := arctable.DefaultArcKey(namespace, arcset.CanonicalizePath(path))
	if err := kv.Put(ctx, key, []byte("not-a-cid")); err != nil {
		t.Fatalf("corrupt stored arc value: %v", err)
	}
}

// Ensure semanticmapping import is exercised even if future edits remove the
// only reference above. Keeps the build honest about test dependencies.
var _ = semanticmapping.NewViewFrom
