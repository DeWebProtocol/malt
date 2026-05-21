package gateway_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/gateway"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	listtree "github.com/dewebprotocol/malt/core/structure/list/tree"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestValidateSemanticMutationRejectsInvalidShape(t *testing.T) {
	root := testCID("root")
	payload := testCID("payload")
	mapDelta := mustCanonicalDelta(t, arcset.KindMap, []deltaChangeSpec{{
		Path:  "@payload",
		After: arcset.NewCASTarget(payload),
	}})
	listDelta := mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{{
		Index: uint64Ptr(0),
		After: arcset.NewCASTarget(payload),
	}})

	tests := []struct {
		name string
		mut  gateway.SemanticMutation
		want error
	}{
		{
			name: "missing base root",
			mut: gateway.SemanticMutation{
				Deltas: []gateway.ArcSetDelta{{
					Object:  root,
					Kind:    arcset.KindMap,
					Changes: mapDelta,
				}},
			},
			want: gateway.ErrInvalidBaseRoot,
		},
		{
			name: "empty deltas",
			mut: gateway.SemanticMutation{
				BaseRoot: root,
			},
			want: gateway.ErrEmptyDeltas,
		},
		{
			name: "nil delta",
			mut: gateway.SemanticMutation{
				BaseRoot: root,
				Deltas: []gateway.ArcSetDelta{{
					Object: root,
					Kind:   arcset.KindMap,
				}},
			},
			want: gateway.ErrNilDelta,
		},
		{
			name: "delta kind mismatch",
			mut: gateway.SemanticMutation{
				BaseRoot: root,
				Deltas: []gateway.ArcSetDelta{{
					Object:  root,
					Kind:    arcset.KindMap,
					Changes: listDelta,
				}},
			},
			want: gateway.ErrObjectKindMismatch,
		},
		{
			name: "object kind mismatch",
			mut: gateway.SemanticMutation{
				BaseRoot: root,
				Deltas: []gateway.ArcSetDelta{{
					Object:  mustTypedRoot(t, codec.SemanticKindList),
					Kind:    arcset.KindMap,
					Changes: mapDelta,
				}},
			},
			want: gateway.ErrObjectKindMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := gateway.ValidateSemanticMutation(tt.mut)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateSemanticMutation error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidateSemanticMutationIsRootCentric(t *testing.T) {
	root := testCID("root-centric-base")
	payload := testCID("root-centric-payload")
	delta := mustCanonicalDelta(t, arcset.KindMap, []deltaChangeSpec{{
		Path:  "@payload",
		After: arcset.NewCASTarget(payload),
	}})

	err := gateway.ValidateSemanticMutation(gateway.SemanticMutation{
		BaseRoot: root,
		Deltas: []gateway.ArcSetDelta{{
			Object:  root,
			Kind:    arcset.KindMap,
			Changes: delta,
		}},
	})
	if err != nil {
		t.Fatalf("ValidateSemanticMutation failed without bucket id: %v", err)
	}
}

func TestCanonicalMapWithoutPayloadIsRejected(t *testing.T) {
	_, err := arcset.NewCanonicalMapArcSet(map[string]cid.Cid{
		"child": testCID("child"),
	})
	if !errors.Is(err, arcset.ErrMissingPayloadBinding) {
		t.Fatalf("NewCanonicalMapArcSet error = %v, want %v", err, arcset.ErrMissingPayloadBinding)
	}

	exec := newExecutor(t)
	_, err = exec.Apply(context.Background(), gateway.SemanticMutation{
		BaseRoot: testCID("base"),
		Deltas: []gateway.ArcSetDelta{{
			Kind: arcset.KindMap,
			Changes: mustCanonicalDelta(t, arcset.KindMap, []deltaChangeSpec{{
				Path:  "child",
				After: arcset.NewMapTarget(testCID("child")),
			}}),
		}},
	})
	if !errors.Is(err, arcset.ErrMissingPayloadBinding) {
		t.Fatalf("map create delta error = %v, want %v", err, arcset.ErrMissingPayloadBinding)
	}
}

func TestExecutorCreatesMapFromDeltaAndReturnsStableReceipt(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	baseRoot := testCID("base")
	payload := testCID("payload")
	child := testCID("child")

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{{
			Kind: arcset.KindMap,
			Changes: mustCanonicalDelta(t, arcset.KindMap, []deltaChangeSpec{
				{Path: "@payload", After: arcset.NewCASTarget(payload)},
				{Path: "child", After: arcset.NewMapTarget(child)},
			}),
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if !receipt.BaseRoot.Equals(baseRoot) {
		t.Fatalf("receipt base root = %s, want %s", receipt.BaseRoot, baseRoot)
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindMap {
		t.Fatalf("new root kind = %s, want %s", codec.SemanticKindOf(receipt.NewRoot), codec.SemanticKindMap)
	}
	if receipt.DeltaCount != 1 {
		t.Fatalf("delta count = %d, want 1", receipt.DeltaCount)
	}
	if receipt.ArcCount != 2 {
		t.Fatalf("arc count = %d, want 2", receipt.ArcCount)
	}

	key := arcset.CanonicalizePath("child")
	binding, proof, err := exec.Maps.Prove(ctx, exec.Namespace, receipt.NewRoot, key)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if !binding.Present || !binding.Value.Equals(child) {
		t.Fatalf("binding = %+v, want child %s", binding, child)
	}
	ok, err := exec.Maps.Verify(receipt.NewRoot, key, binding, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !ok {
		t.Fatal("expected map proof to verify")
	}
}

func TestExecutorCreatesListFromDeltaAndReturnsStableReceipt(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	baseRoot := testCID("base")
	first := testCID("first")
	second := testCID("second")

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{{
			Kind: arcset.KindList,
			Changes: mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{
				{Index: uint64Ptr(0), After: arcset.NewCASTarget(first)},
				{Index: uint64Ptr(1), After: arcset.NewCASTarget(second)},
			}),
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindList {
		t.Fatalf("new root kind = %s, want %s", codec.SemanticKindOf(receipt.NewRoot), codec.SemanticKindList)
	}
	if receipt.DeltaCount != 1 {
		t.Fatalf("delta count = %d, want 1", receipt.DeltaCount)
	}
	if receipt.ArcCount != 2 {
		t.Fatalf("arc count = %d, want 2", receipt.ArcCount)
	}

	query, proof, err := exec.Lists.Prove(ctx, exec.Namespace, receipt.NewRoot, 1)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if query.Length != 2 || !query.Key.Equals(second) {
		t.Fatalf("query = %+v, want length 2 and key %s", query, second)
	}
	ok, err := exec.Lists.Verify(receipt.NewRoot, 1, query, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !ok {
		t.Fatal("expected list proof to verify")
	}
}

func TestExecutorRejectsListLengthProofWhenVerifyReturnsFalse(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	first := testCID("first")
	second := testCID("second")
	inserted := testCID("inserted")
	baseRoot, err := exec.Lists.Commit(ctx, exec.Namespace, list.NewViewFromSlice([]cid.Cid{first, second}))
	if err != nil {
		t.Fatalf("Commit base list failed: %v", err)
	}
	exec.Lists = &verifyFalseList{
		Semantics: exec.Lists,
		reject: func(index uint64, call int) bool {
			return call == 1 && index == 0
		},
	}

	_, err = exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{{
			Object: baseRoot,
			Kind:   arcset.KindList,
			Changes: mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{{
				Index: uint64Ptr(0),
				After: arcset.NewCASTarget(inserted),
			}}),
		}},
	})
	if err == nil {
		t.Fatal("Apply succeeded with invalid list length proof")
	}
	if !strings.Contains(err.Error(), "list length proof failed") {
		t.Fatalf("Apply error = %v, want list length proof failure", err)
	}
}

func TestExecutorRejectsListPreconditionProofWhenVerifyReturnsFalse(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	first := testCID("first")
	oldSecond := testCID("old-second")
	newSecond := testCID("new-second")
	baseRoot, err := exec.Lists.Commit(ctx, exec.Namespace, list.NewViewFromSlice([]cid.Cid{first, oldSecond}))
	if err != nil {
		t.Fatalf("Commit base list failed: %v", err)
	}
	exec.Lists = &verifyFalseList{
		Semantics: exec.Lists,
		reject: func(index uint64, call int) bool {
			return index == 1
		},
	}

	_, err = exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{{
			Object: baseRoot,
			Kind:   arcset.KindList,
			Changes: mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{{
				Index:  uint64Ptr(1),
				Before: arcset.NewCASTarget(oldSecond),
				After:  arcset.NewCASTarget(newSecond),
			}}),
		}},
	})
	if err == nil {
		t.Fatal("Apply succeeded with invalid list precondition proof")
	}
	if !strings.Contains(err.Error(), "list proof failed at index 1") {
		t.Fatalf("Apply error = %v, want list precondition proof failure", err)
	}
}

func TestExecutorReplaysFixedMeasuredListToExpectedRoot(t *testing.T) {
	ctx := context.Background()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("kzg.NewScheme failed: %v", err)
	}
	chunks := []cid.Cid{testCID("chunk-0"), testCID("chunk-1")}
	chunkSize := uint64(4)
	totalSize := uint64(7)

	sourceArcs, err := overwrite.NewArcTable(overwrite.WithKVStore(kvmemory.New()))
	if err != nil {
		t.Fatalf("source overwrite.NewArcTable failed: %v", err)
	}
	sourceLists, err := listtree.NewList(scheme, sourceArcs)
	if err != nil {
		t.Fatalf("source tree.NewList failed: %v", err)
	}
	expectedRoot, err := sourceLists.CommitFixed(ctx, "source", chunks, chunkSize, totalSize)
	if err != nil {
		t.Fatalf("CommitFixed failed: %v", err)
	}

	replayArcs, err := overwrite.NewArcTable(overwrite.WithKVStore(kvmemory.New()))
	if err != nil {
		t.Fatalf("replay overwrite.NewArcTable failed: %v", err)
	}
	replayMaps, err := mappingradix.NewMap(scheme, replayArcs)
	if err != nil {
		t.Fatalf("replay radix.NewMap failed: %v", err)
	}
	replayLists, err := listtree.NewList(scheme, replayArcs)
	if err != nil {
		t.Fatalf("replay tree.NewList failed: %v", err)
	}
	exec := gateway.Executor{
		Namespace: "measured-replay",
		Maps:      replayMaps,
		Lists:     replayLists,
		ArcTable:  replayArcs,
	}

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: testCID("base"),
		Deltas: []gateway.ArcSetDelta{{
			ExpectedRoot: expectedRoot,
			Kind:         arcset.KindList,
			Changes: mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{
				{Index: uint64Ptr(0), After: arcset.NewCASTarget(chunks[0])},
				{Index: uint64Ptr(1), After: arcset.NewCASTarget(chunks[1])},
			}),
			Commit: gateway.CommitDescriptor{
				FixedList: &gateway.FixedListCommit{
					TotalSize: totalSize,
					ChunkSize: chunkSize,
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if !receipt.NewRoot.Equals(expectedRoot) {
		t.Fatalf("replayed root = %s, want expected measured root %s", receipt.NewRoot, expectedRoot)
	}

	measured := exec.Lists.(list.MeasuredSemantics)
	end := totalSize
	result, proof, err := measured.ProveRange(ctx, exec.Namespace, expectedRoot, 0, &end)
	if err != nil {
		t.Fatalf("ProveRange on replayed root failed: %v", err)
	}
	ok, err := measured.VerifyRange(expectedRoot, 0, &end, result, proof)
	if err != nil {
		t.Fatalf("VerifyRange on replayed root failed: %v", err)
	}
	if !ok {
		t.Fatal("VerifyRange on replayed root returned false")
	}
}

func TestExecutorAppliesDeltasInOrderAndUsesFinalRoot(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	baseRoot := testCID("base")
	payload := testCID("payload")
	chunk := testCID("chunk")

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{
			{
				Kind: arcset.KindMap,
				Changes: mustCanonicalDelta(t, arcset.KindMap, []deltaChangeSpec{{
					Path:  "@payload",
					After: arcset.NewCASTarget(payload),
				}}),
			},
			{
				Kind: arcset.KindList,
				Changes: mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{{
					Index: uint64Ptr(0),
					After: arcset.NewCASTarget(chunk),
				}}),
			},
		},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindList {
		t.Fatalf("new root kind = %s, want final delta kind %s", codec.SemanticKindOf(receipt.NewRoot), codec.SemanticKindList)
	}
	if receipt.DeltaCount != 2 {
		t.Fatalf("delta count = %d, want 2", receipt.DeltaCount)
	}
	if receipt.ArcCount != 2 {
		t.Fatalf("arc count = %d, want 2", receipt.ArcCount)
	}
}

func newExecutor(t *testing.T) gateway.Executor {
	t.Helper()

	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("kzg.NewScheme failed: %v", err)
	}
	arcs, err := overwrite.NewArcTable(overwrite.WithKVStore(kvmemory.New()))
	if err != nil {
		t.Fatalf("overwrite.NewArcTable failed: %v", err)
	}
	maps, err := mappingradix.NewMap(scheme, arcs)
	if err != nil {
		t.Fatalf("radix.NewMap failed: %v", err)
	}
	lists, err := listtree.NewList(scheme, arcs)
	if err != nil {
		t.Fatalf("tree.NewList failed: %v", err)
	}
	return gateway.Executor{
		Namespace: "gateway-test",
		Maps:      maps,
		Lists:     lists,
		ArcTable:  arcs,
	}
}

type verifyFalseList struct {
	list.Semantics
	reject func(index uint64, call int) bool
	calls  int
}

func (l *verifyFalseList) Verify(root cid.Cid, index uint64, expected list.Query, proof structure.Proof) (bool, error) {
	l.calls++
	if l.reject != nil && l.reject(index, l.calls) {
		return false, nil
	}
	return l.Semantics.Verify(root, index, expected, proof)
}

func mustCanonicalMap(t *testing.T, entries map[string]cid.Cid) *arcset.CanonicalArcSet {
	t.Helper()

	set, err := arcset.NewCanonicalMapArcSet(entries)
	if err != nil {
		t.Fatalf("NewCanonicalMapArcSet failed: %v", err)
	}
	return set
}

func mustCanonicalList(t *testing.T, values []cid.Cid) *arcset.CanonicalArcSet {
	t.Helper()

	set, err := arcset.NewCanonicalListArcSet(values)
	if err != nil {
		t.Fatalf("NewCanonicalListArcSet failed: %v", err)
	}
	return set
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

func testCID(seed string) cid.Cid {
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}
