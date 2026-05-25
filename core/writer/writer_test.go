package writer

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/structure/list"
	listtree "github.com/dewebprotocol/malt/core/structure/list/tree"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Test helpers.

func newTestWriter(t *testing.T) (*Writer, *overwrite.ArcTable, mapping.Semantics, *kvg) {
	t.Helper()

	// Memory KVStore
	kv := kvmemory.New()

	// Overwrite ArcTable
	e, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("failed to create ArcTable: %v", err)
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

	w := NewWriter(semantic, e)

	return w, e, semantic, kv
}

func newTestWriterWithList(t *testing.T) (*Writer, *overwrite.ArcTable, mapping.Semantics, list.Semantics, *kvg) {
	t.Helper()

	kv := kvmemory.New()
	e, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("failed to create ArcTable: %v", err)
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("failed to create KZG scheme: %v", err)
	}
	semantic, err := mappingradix.NewMap(scheme, e)
	if err != nil {
		t.Fatalf("failed to create mapping semantic: %v", err)
	}
	listSemantic, err := listtree.NewList(scheme, e)
	if err != nil {
		t.Fatalf("failed to create list semantic: %v", err)
	}

	w := NewWriter(semantic, e, listSemantic)
	return w, e, semantic, listSemantic, kv
}

type kvg = kvmemory.KV

func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func mustTypedRoot(t *testing.T, kind codec.SemanticKind) cid.Cid {
	t.Helper()

	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("kzg.NewScheme failed: %v", err)
	}
	root, err := scheme.Commit(nil)
	if err != nil {
		t.Fatalf("scheme.Commit failed: %v", err)
	}
	commitment, err := codec.ExtractCommitment(root)
	if err != nil {
		t.Fatalf("ExtractCommitment failed: %v", err)
	}
	typed, err := codec.NewTypedCID(kind, codec.BackendKindKZG, commitment)
	if err != nil {
		t.Fatalf("NewTypedCID failed: %v", err)
	}
	return typed
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

func mustWriterDelta(t *testing.T, kind arcset.Kind, changes []arcset.ArcChange) *arcset.CanonicalArcDelta {
	t.Helper()
	delta, err := arcset.NewCanonicalArcDelta(kind, changes)
	if err != nil {
		t.Fatalf("NewCanonicalArcDelta failed: %v", err)
	}
	return delta
}

func mustMapCoordinate(t *testing.T, path string) arcset.CanonicalCoordinate {
	t.Helper()
	coord, err := arcset.NewMapCoordinate(path)
	if err != nil {
		t.Fatalf("NewMapCoordinate failed: %v", err)
	}
	return coord
}

func mustListCoordinate(t *testing.T, index int64) arcset.CanonicalCoordinate {
	t.Helper()
	coord, err := arcset.NewListCoordinate(index)
	if err != nil {
		t.Fatalf("NewListCoordinate failed: %v", err)
	}
	return coord
}

func targetRefPtr(target arcset.TargetRef) *arcset.TargetRef {
	return &target
}

// Tests.

func TestValidateSemanticMutationRejectsInvalidShape(t *testing.T) {
	root := fakeCID("root")
	payload := fakeCID("payload")
	mapDelta := mustWriterDelta(t, arcset.KindMap, []arcset.ArcChange{{
		Coordinate: mustMapCoordinate(t, "@payload"),
		After:      targetRefPtr(arcset.NewCASTarget(payload)),
	}})
	listDelta := mustWriterDelta(t, arcset.KindList, []arcset.ArcChange{{
		Coordinate: mustListCoordinate(t, 0),
		After:      targetRefPtr(arcset.NewCASTarget(payload)),
	}})

	tests := []struct {
		name string
		mut  SemanticMutation
		want error
	}{
		{
			name: "missing base root",
			mut: SemanticMutation{
				Deltas: []ArcSetDelta{{
					Object:  root,
					Kind:    arcset.KindMap,
					Changes: mapDelta,
				}},
			},
			want: ErrInvalidBaseRoot,
		},
		{
			name: "empty deltas",
			mut: SemanticMutation{
				BaseRoot: root,
			},
			want: ErrEmptyDeltas,
		},
		{
			name: "nil delta",
			mut: SemanticMutation{
				BaseRoot: root,
				Deltas: []ArcSetDelta{{
					Object: root,
					Kind:   arcset.KindMap,
				}},
			},
			want: ErrNilDelta,
		},
		{
			name: "delta kind mismatch",
			mut: SemanticMutation{
				BaseRoot: root,
				Deltas: []ArcSetDelta{{
					Object:  root,
					Kind:    arcset.KindMap,
					Changes: listDelta,
				}},
			},
			want: ErrObjectKindMismatch,
		},
		{
			name: "object kind mismatch",
			mut: SemanticMutation{
				BaseRoot: root,
				Deltas: []ArcSetDelta{{
					Object:  mustTypedRoot(t, codec.SemanticKindList),
					Kind:    arcset.KindMap,
					Changes: mapDelta,
				}},
			},
			want: ErrObjectKindMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSemanticMutation(tt.mut)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateSemanticMutation error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidateSemanticMutationIsRootCentric(t *testing.T) {
	root := fakeCID("root-centric-base")
	payload := fakeCID("root-centric-payload")
	delta := mustWriterDelta(t, arcset.KindMap, []arcset.ArcChange{{
		Coordinate: mustMapCoordinate(t, "@payload"),
		After:      targetRefPtr(arcset.NewCASTarget(payload)),
	}})

	err := ValidateSemanticMutation(SemanticMutation{
		BaseRoot: root,
		Deltas: []ArcSetDelta{{
			Object:  root,
			Kind:    arcset.KindMap,
			Changes: delta,
		}},
	})
	if err != nil {
		t.Fatalf("ValidateSemanticMutation failed without bucket id: %v", err)
	}
}

func TestWriterApplySemanticMapMutation(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	namespace := "test"
	oldChild := fakeCID("old-child")
	newChild := fakeCID("new-child")

	root, err := w.CreateStructure(ctx, namespace, makeArcSet(map[string]cid.Cid{
		"child": oldChild,
	}))
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	receipt, err := w.Apply(ctx, namespace, SemanticMutation{
		BaseRoot: root,
		Deltas: []ArcSetDelta{{
			Object: root,
			Kind:   arcset.KindMap,
			Changes: mustWriterDelta(t, arcset.KindMap, []arcset.ArcChange{{
				Coordinate: mustMapCoordinate(t, "child"),
				Before:     targetRefPtr(arcset.NewMapTarget(oldChild)),
				After:      targetRefPtr(arcset.NewMapTarget(newChild)),
			}}),
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if receipt.DeltaCount != 1 || receipt.ArcCount != 1 {
		t.Fatalf("receipt counts = deltas %d arcs %d, want 1/1", receipt.DeltaCount, receipt.ArcCount)
	}

	got, err := w.GetArc(ctx, namespace, receipt.NewRoot, "child")
	if err != nil {
		t.Fatalf("GetArc failed: %v", err)
	}
	if !got.Equals(newChild) {
		t.Fatalf("child target = %s, want %s", got, newChild)
	}
}

func TestWriterApplySemanticListCreateMutation(t *testing.T) {
	w, _, _, lists, _ := newTestWriterWithList(t)
	ctx := context.Background()
	namespace := "test"
	first := fakeCID("first")
	second := fakeCID("second")

	receipt, err := w.Apply(ctx, namespace, SemanticMutation{
		BaseRoot: fakeCID("base"),
		Deltas: []ArcSetDelta{{
			Kind: arcset.KindList,
			Changes: mustWriterDelta(t, arcset.KindList, []arcset.ArcChange{
				{Coordinate: mustListCoordinate(t, 0), After: targetRefPtr(arcset.NewCASTarget(first))},
				{Coordinate: mustListCoordinate(t, 1), After: targetRefPtr(arcset.NewCASTarget(second))},
			}),
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	query, proof, err := lists.Prove(ctx, namespace, receipt.NewRoot, 1)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if query.Length != 2 || !query.Key.Equals(second) {
		t.Fatalf("query = %+v, want length 2 key %s", query, second)
	}
	ok, err := lists.Verify(receipt.NewRoot, 1, query, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !ok {
		t.Fatal("list proof did not verify")
	}
}

func TestWriterApplySemanticListAppendMutation(t *testing.T) {
	w, _, _, lists, _ := newTestWriterWithList(t)
	ctx := context.Background()
	namespace := "test"
	first := fakeCID("first")
	second := fakeCID("second")
	third := fakeCID("third")
	root, err := lists.Commit(ctx, namespace, list.NewViewFromSlice([]cid.Cid{first, second}))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	receipt, err := w.Apply(ctx, namespace, SemanticMutation{
		BaseRoot: root,
		Deltas: []ArcSetDelta{{
			Object: root,
			Kind:   arcset.KindList,
			Changes: mustWriterDelta(t, arcset.KindList, []arcset.ArcChange{{
				Coordinate: mustListCoordinate(t, 2),
				After:      targetRefPtr(arcset.NewCASTarget(third)),
			}}),
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	query, proof, err := lists.Prove(ctx, namespace, receipt.NewRoot, 2)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if query.Length != 3 || !query.Key.Equals(third) {
		t.Fatalf("query = %+v, want length 3 key %s", query, third)
	}
	ok, err := lists.Verify(receipt.NewRoot, 2, query, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !ok {
		t.Fatal("list proof did not verify")
	}
}

func TestWriterApplyFixedMeasuredListAppendMutation(t *testing.T) {
	w, _, _, lists, _ := newTestWriterWithList(t)
	ctx := context.Background()
	namespace := "test"
	chunkSize := uint64(4)
	chunks := make([]cid.Cid, 256)
	for i := range chunks {
		chunks[i] = fakeCID("fixed-chunk-" + strconv.Itoa(i))
	}
	fixed := lists.(interface {
		CommitFixed(context.Context, string, []cid.Cid, uint64, uint64) (cid.Cid, error)
	})
	baseRoot, err := fixed.CommitFixed(ctx, namespace, chunks[:255], chunkSize, uint64(255)*chunkSize)
	if err != nil {
		t.Fatalf("CommitFixed base failed: %v", err)
	}
	expectedRoot, err := fixed.CommitFixed(ctx, namespace, chunks, chunkSize, uint64(256)*chunkSize)
	if err != nil {
		t.Fatalf("CommitFixed expected failed: %v", err)
	}

	receipt, err := w.Apply(ctx, namespace, SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []ArcSetDelta{{
			Object:       baseRoot,
			ExpectedRoot: expectedRoot,
			Kind:         arcset.KindList,
			Changes: mustWriterDelta(t, arcset.KindList, []arcset.ArcChange{{
				Coordinate: mustListCoordinate(t, 255),
				After:      targetRefPtr(arcset.NewCASTarget(chunks[255])),
			}}),
			Commit: CommitDescriptor{
				FixedList: &FixedListCommit{
					TotalSize: uint64(256) * chunkSize,
					ChunkSize: chunkSize,
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if !receipt.NewRoot.Equals(expectedRoot) {
		t.Fatalf("new root = %s, want %s", receipt.NewRoot, expectedRoot)
	}
}

func TestWriter_UpdateArc_Insert(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	namespace := "test"

	// Create initial structure
	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
	})
	root, err := w.CreateStructure(ctx, namespace, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}
	if !root.Defined() {
		t.Fatal("root not defined after CreateStructure")
	}

	// Insert a new arc
	newTarget := fakeCID("data-c")
	result, err := w.UpdateArc(ctx, namespace, root, "c", newTarget)
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
	target, err := w.GetArc(ctx, namespace, result.NewRoot, "c")
	if err != nil {
		t.Fatalf("GetArc failed after insert: %v", err)
	}
	if target != newTarget {
		t.Errorf("getArc returned wrong target: expected %s, got %s", newTarget, target)
	}

	// Verify old arcs are still accessible
	for path, expected := range map[string]cid.Cid{"a": fakeCID("data-a"), "b": fakeCID("data-b")} {
		got, err := w.GetArc(ctx, namespace, result.NewRoot, path)
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
	namespace := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
	})
	root, err := w.CreateStructure(ctx, namespace, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Replace arc "a"
	newTarget := fakeCID("data-a-new")
	result, err := w.UpdateArc(ctx, namespace, root, "a", newTarget)
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
	got, err := w.GetArc(ctx, namespace, result.NewRoot, "a")
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
	namespace := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
	})
	root, err := w.CreateStructure(ctx, namespace, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Delete arc "a"
	result, err := w.UpdateArc(ctx, namespace, root, "a", cid.Undef)
	if err != nil {
		t.Fatalf("UpdateArc delete failed: %v", err)
	}

	if result.Op != ArcDelete {
		t.Errorf("expected ArcDelete, got %v", result.Op)
	}

	// Verify deleted
	_, err = w.GetArc(ctx, namespace, result.NewRoot, "a")
	if err == nil {
		t.Error("expected error after delete, got nil")
	}

	// Arc "b" should still be accessible
	got, err := w.GetArc(ctx, namespace, result.NewRoot, "b")
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
	namespace := "test"

	// Undefined root
	_, err := w.UpdateArc(ctx, namespace, cid.Undef, "a", fakeCID("data"))
	if err != ErrInvalidRoot {
		t.Errorf("expected ErrInvalidRoot, got %v", err)
	}

	// Empty path
	arcs := makeArcSet(map[string]cid.Cid{"a": fakeCID("data-a")})
	root, _ := w.CreateStructure(ctx, namespace, arcs)
	_, err = w.UpdateArc(ctx, namespace, root, "", fakeCID("data"))
	if err != ErrEmptyPath {
		t.Errorf("expected ErrEmptyPath, got %v", err)
	}
}

func TestWriter_BatchUpdateArcs(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	namespace := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"a": fakeCID("data-a"),
		"b": fakeCID("data-b"),
		"c": fakeCID("data-c"),
	})
	root, err := w.CreateStructure(ctx, namespace, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Batch update: replace "a", insert "d", delete "c"
	updates := map[string]cid.Cid{
		"a": fakeCID("data-a-new"),
		"d": fakeCID("data-d"),
		"c": cid.Undef,
	}

	result, err := w.BatchUpdateArcs(ctx, namespace, root, updates)
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
		got, err := w.GetArc(ctx, namespace, result.NewRoot, path)
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
	namespace := "test"

	// Undefined root
	_, err := w.BatchUpdateArcs(ctx, namespace, cid.Undef, map[string]cid.Cid{"a": fakeCID("data")})
	if err != ErrInvalidRoot {
		t.Errorf("expected ErrInvalidRoot, got %v", err)
	}

	// Empty updates
	arcs := makeArcSet(map[string]cid.Cid{"a": fakeCID("data-a")})
	root, _ := w.CreateStructure(ctx, namespace, arcs)
	_, err = w.BatchUpdateArcs(ctx, namespace, root, map[string]cid.Cid{})
	if err == nil {
		t.Error("expected error for empty updates, got nil")
	}
}

func TestWriter_CreateStructure(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	namespace := "test"

	// Create structure with arcs
	arcs := makeArcSet(map[string]cid.Cid{
		"foo": fakeCID("data-foo"),
		"bar": fakeCID("data-bar"),
	})
	root, err := w.CreateStructure(ctx, namespace, arcs)
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
		got, err := w.GetArc(ctx, namespace, root, path)
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
	namespace := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"/foo//bar/": fakeCID("data-foo-bar"),
	})
	root, err := w.CreateStructure(ctx, namespace, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	got, err := w.GetArc(ctx, namespace, root, "foo/bar")
	if err != nil {
		t.Fatalf("GetArc failed: %v", err)
	}
	if got != fakeCID("data-foo-bar") {
		t.Errorf("GetArc(foo/bar): expected %s, got %s", fakeCID("data-foo-bar"), got)
	}

	updated, err := w.UpdateArc(ctx, namespace, root, "/foo//bar/", fakeCID("data-foo-bar-v2"))
	if err != nil {
		t.Fatalf("UpdateArc failed: %v", err)
	}
	got, err = w.GetArc(ctx, namespace, updated.NewRoot, "/foo//bar/")
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
	namespace := "test"

	_, err := w.CreateStructure(ctx, namespace, nil)
	if err == nil {
		t.Error("expected error for nil arc set, got nil")
	}
}

func TestWriter_GetArc_NotFound(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	namespace := "test"

	arcs := makeArcSet(map[string]cid.Cid{"a": fakeCID("data-a")})
	root, _ := w.CreateStructure(ctx, namespace, arcs)

	_, err := w.GetArc(ctx, namespace, root, "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent arc, got nil")
	}
}

func TestWriter_GetSnapshot(t *testing.T) {
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	namespace := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"x": fakeCID("data-x"),
		"y": fakeCID("data-y"),
	})
	root, err := w.CreateStructure(ctx, namespace, arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	snapshot, err := w.GetSnapshot(ctx, namespace, root)
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

func TestWriter_UpdateArc_UpdateThenGet(t *testing.T) {
	// Verify that after multiple updates, the latest structure root
	// reflects all accumulated changes.
	w, _, _, _ := newTestWriter(t)
	ctx := context.Background()
	namespace := "test"

	arcs := makeArcSet(map[string]cid.Cid{
		"alpha": fakeCID("v0-alpha"),
	})
	root, _ := w.CreateStructure(ctx, namespace, arcs)

	// Update 1: insert "beta"
	r1, err := w.UpdateArc(ctx, namespace, root, "beta", fakeCID("v0-beta"))
	if err != nil {
		t.Fatalf("Update 1 (insert beta) failed: %v", err)
	}

	// Update 2: replace "alpha"
	r2, err := w.UpdateArc(ctx, namespace, r1.NewRoot, "alpha", fakeCID("v1-alpha"))
	if err != nil {
		t.Fatalf("Update 2 (replace alpha) failed: %v", err)
	}

	// Update 3: insert "gamma"
	r3, err := w.UpdateArc(ctx, namespace, r2.NewRoot, "gamma", fakeCID("v0-gamma"))
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
		got, err := w.GetArc(ctx, namespace, finalRoot, path)
		if err != nil {
			t.Fatalf("GetArc(%s) after chain of updates: %v", path, err)
		}
		if got != expected {
			t.Errorf("GetArc(%s) after updates: expected %s, got %s", path, expected, got)
		}
	}
}
