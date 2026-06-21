package writer

import (
	"context"
	"errors"
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

	// The semantic root is valid but unreadable via the index before retry:
	// GetArc against newRoot must fail because the index write never landed.
	if _, err := w.GetArc(ctx, namespace, idxErr.NewRoot, "a"); err == nil {
		t.Error("GetArc(newRoot, a) succeeded before retry; index write should be missing")
	}

	// Recovery is by re-running the original writer operation (not by replaying
	// an ArcTable delta from the error — the delta is intentionally not carried
	// by IndexWriteFailedError). With the ArcTable healthy again, re-running
	// UpdateArc against the same base root and inputs must converge to the same
	// content-addressed newRoot and then publish its index entry.
	retryResult, err := w.UpdateArc(ctx, namespace, root, "a", newValueA)
	if err != nil {
		t.Fatalf("retry UpdateArc: %v", err)
	}
	if !retryResult.NewRoot.Equals(idxErr.NewRoot) {
		t.Errorf("retry produced newRoot %s, want deterministic %s (idempotency broken)",
			retryResult.NewRoot, idxErr.NewRoot)
	}

	// Now newRoot is readable.
	got, err := w.GetArc(ctx, namespace, retryResult.NewRoot, "a")
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

	// Retry converges to the same root (content-addressed determinism).
	retry, err := w.BatchUpdateArcs(ctx, namespace, root, map[string]cid.Cid{
		"a": newA,
		"b": newB,
	})
	if err != nil {
		t.Fatalf("retry BatchUpdateArcs: %v", err)
	}
	if !retry.NewRoot.Equals(idxErr.NewRoot) {
		t.Errorf("retry newRoot %s != failed newRoot %s", retry.NewRoot, idxErr.NewRoot)
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

func makeCIDLocal(t *testing.T, data string) cid.Cid {
	t.Helper()
	mhash, err := mh.Sum([]byte(data), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("mh.Sum: %v", err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}

// Ensure semanticmapping import is exercised even if future edits remove the
// only reference above. Keeps the build honest about test dependencies.
var _ = semanticmapping.NewViewFrom
