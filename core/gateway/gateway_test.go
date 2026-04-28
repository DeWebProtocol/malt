package gateway_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/gateway"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	listtree "github.com/dewebprotocol/malt/core/structure/list/tree"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestValidateSemanticMutationRejectsInvalidShape(t *testing.T) {
	root := testCID("root")
	payload := testCID("payload")
	mapSet := mustCanonicalMap(t, map[string]cid.Cid{"@payload": payload})
	listSet := mustCanonicalList(t, []cid.Cid{payload})

	tests := []struct {
		name string
		mut  gateway.SemanticMutation
		want error
	}{
		{
			name: "missing bucket",
			mut: gateway.SemanticMutation{
				BaseRoot: root,
				Puts: []gateway.ArcSetPut{{
					Object: root,
					Kind:   arcset.KindMap,
					ArcSet: mapSet,
				}},
			},
			want: gateway.ErrInvalidBucket,
		},
		{
			name: "missing base root",
			mut: gateway.SemanticMutation{
				BucketID: "bucket",
				Puts: []gateway.ArcSetPut{{
					Object: root,
					Kind:   arcset.KindMap,
					ArcSet: mapSet,
				}},
			},
			want: gateway.ErrInvalidBaseRoot,
		},
		{
			name: "empty puts",
			mut: gateway.SemanticMutation{
				BucketID: "bucket",
				BaseRoot: root,
			},
			want: gateway.ErrEmptyPuts,
		},
		{
			name: "nil arcset",
			mut: gateway.SemanticMutation{
				BucketID: "bucket",
				BaseRoot: root,
				Puts: []gateway.ArcSetPut{{
					Object: root,
					Kind:   arcset.KindMap,
				}},
			},
			want: gateway.ErrNilArcSet,
		},
		{
			name: "put kind mismatch",
			mut: gateway.SemanticMutation{
				BucketID: "bucket",
				BaseRoot: root,
				Puts: []gateway.ArcSetPut{{
					Object: root,
					Kind:   arcset.KindMap,
					ArcSet: listSet,
				}},
			},
			want: gateway.ErrObjectKindMismatch,
		},
		{
			name: "object kind mismatch",
			mut: gateway.SemanticMutation{
				BucketID: "bucket",
				BaseRoot: root,
				Puts: []gateway.ArcSetPut{{
					Object: mustTypedRoot(t, codec.SemanticKindList),
					Kind:   arcset.KindMap,
					ArcSet: mapSet,
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

func TestCanonicalMapWithoutPayloadIsRejected(t *testing.T) {
	_, err := arcset.NewCanonicalMapArcSet(map[string]cid.Cid{
		"child": testCID("child"),
	})
	if !errors.Is(err, arcset.ErrMissingPayloadBinding) {
		t.Fatalf("NewCanonicalMapArcSet error = %v, want %v", err, arcset.ErrMissingPayloadBinding)
	}
}

func TestExecutorAppliesMapReplacementAndReturnsStableReceipt(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	bucketID := "gateway-map-replacement"
	baseRoot := testCID("base")
	payload := testCID("payload")
	child := testCID("child")
	set := mustCanonicalMap(t, map[string]cid.Cid{
		"@payload": payload,
		"child":    child,
	})

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BucketID: bucketID,
		BaseRoot: baseRoot,
		Puts: []gateway.ArcSetPut{{
			Object: baseRoot,
			Kind:   arcset.KindMap,
			ArcSet: set,
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if receipt.BucketID != bucketID {
		t.Fatalf("receipt bucket = %q, want %q", receipt.BucketID, bucketID)
	}
	if !receipt.BaseRoot.Equals(baseRoot) {
		t.Fatalf("receipt base root = %s, want %s", receipt.BaseRoot, baseRoot)
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindMap {
		t.Fatalf("new root kind = %s, want %s", codec.SemanticKindOf(receipt.NewRoot), codec.SemanticKindMap)
	}
	if receipt.PutCount != 1 {
		t.Fatalf("put count = %d, want 1", receipt.PutCount)
	}
	if receipt.ArcCount != 2 {
		t.Fatalf("arc count = %d, want 2", receipt.ArcCount)
	}

	key := arcset.CanonicalizePath("child")
	binding, proof, err := exec.Maps.Prove(ctx, bucketID, receipt.NewRoot, key)
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

func TestExecutorAppliesListReplacementAndReturnsStableReceipt(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	bucketID := "gateway-list-replacement"
	baseRoot := testCID("base")
	first := testCID("first")
	second := testCID("second")
	set := mustCanonicalList(t, []cid.Cid{first, second})

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BucketID: bucketID,
		BaseRoot: baseRoot,
		Puts: []gateway.ArcSetPut{{
			Object: baseRoot,
			Kind:   arcset.KindList,
			ArcSet: set,
		}},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindList {
		t.Fatalf("new root kind = %s, want %s", codec.SemanticKindOf(receipt.NewRoot), codec.SemanticKindList)
	}
	if receipt.PutCount != 1 {
		t.Fatalf("put count = %d, want 1", receipt.PutCount)
	}
	if receipt.ArcCount != 2 {
		t.Fatalf("arc count = %d, want 2", receipt.ArcCount)
	}

	query, proof, err := exec.Lists.Prove(ctx, bucketID, receipt.NewRoot, 1)
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

func TestExecutorAppliesPutsInOrderAndUsesFinalRoot(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	bucketID := "gateway-ordered-replacements"
	baseRoot := testCID("base")
	mapSet := mustCanonicalMap(t, map[string]cid.Cid{
		"@payload": testCID("payload"),
	})
	listSet := mustCanonicalList(t, []cid.Cid{testCID("chunk")})

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BucketID: bucketID,
		BaseRoot: baseRoot,
		Puts: []gateway.ArcSetPut{
			{Object: baseRoot, Kind: arcset.KindMap, ArcSet: mapSet},
			{Object: cid.Undef, Kind: arcset.KindList, ArcSet: listSet},
		},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindList {
		t.Fatalf("new root kind = %s, want final put kind %s", codec.SemanticKindOf(receipt.NewRoot), codec.SemanticKindList)
	}
	if receipt.PutCount != 2 {
		t.Fatalf("put count = %d, want 2", receipt.PutCount)
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
		Maps:     maps,
		Lists:    lists,
		ArcTable: arcs,
	}
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
