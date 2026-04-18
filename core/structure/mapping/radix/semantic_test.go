package radix_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/kvstore/memory"
	backendradix "github.com/dewebprotocol/malt/core/sce/commitment/radix"
	mapping "github.com/dewebprotocol/malt/core/structure/mapping"
	semanticradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func newPayloadCID(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func TestRadixMappingSemanticProofs(t *testing.T) {
	ctx := context.Background()
	backend, err := backendradix.NewScheme(backendradix.WithKVStore(memory.New()))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	semantic, err := semanticradix.New(backend)
	if err != nil {
		t.Fatalf("semanticradix.New failed: %v", err)
	}

	valueA := newPayloadCID([]byte("a"))
	valueAB := newPayloadCID([]byte("ab"))
	view := mapping.NewViewFrom(map[string]cid.Cid{
		"a":   valueA,
		"a/b": valueAB,
	})

	root, err := semantic.Commit(ctx, view)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	binding, proof, err := semantic.Prove(ctx, root, view, arcset.CanonicalizePath("a/b"))
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if !binding.Present || !binding.Value.Equals(valueAB) {
		t.Fatalf("unexpected binding: %+v", binding)
	}

	ok, err := semantic.Verify(root, arcset.CanonicalizePath("a/b"), binding, proof)
	if err != nil || !ok {
		t.Fatalf("Verify failed: ok=%v err=%v", ok, err)
	}

	absent, absentProof, err := semantic.Prove(ctx, root, view, arcset.CanonicalizePath("missing"))
	if err != nil {
		t.Fatalf("Prove absent failed: %v", err)
	}
	if absent.Present {
		t.Fatalf("expected non-membership proof, got %+v", absent)
	}

	ok, err = semantic.Verify(root, arcset.CanonicalizePath("missing"), absent, absentProof)
	if err != nil || !ok {
		t.Fatalf("Verify absent failed: ok=%v err=%v", ok, err)
	}
}

func TestRadixMappingSemanticUpdate(t *testing.T) {
	ctx := context.Background()
	backend, err := backendradix.NewScheme(backendradix.WithKVStore(memory.New()))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	semantic, err := semanticradix.New(backend)
	if err != nil {
		t.Fatalf("semanticradix.New failed: %v", err)
	}

	oldValue := newPayloadCID([]byte("old"))
	newValue := newPayloadCID([]byte("new"))
	view := mapping.NewViewFrom(map[string]cid.Cid{"k": oldValue})

	root, err := semantic.Commit(ctx, view)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	newRoot, err := semantic.Update(ctx, root, view, arcset.CanonicalizePath("k"), oldValue, newValue)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	updatedView := mapping.NewViewFrom(map[string]cid.Cid{"k": newValue})
	binding, proof, err := semantic.Prove(ctx, newRoot, updatedView, arcset.CanonicalizePath("k"))
	if err != nil {
		t.Fatalf("Prove updated failed: %v", err)
	}

	ok, err := semantic.Verify(newRoot, arcset.CanonicalizePath("k"), binding, proof)
	if err != nil || !ok {
		t.Fatalf("Verify updated failed: ok=%v err=%v", ok, err)
	}
}
