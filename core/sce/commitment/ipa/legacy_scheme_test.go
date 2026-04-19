package ipa_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

func TestIPALegacySchemeProveAndVerify(t *testing.T) {
	scheme, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	target := newIndexedPayloadCID([]byte("value"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"item": target})

	root, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	proved, proof, err := scheme.Prove(root, arcs, "item")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if !proved.Equals(target) {
		t.Fatalf("unexpected proved value %s", proved)
	}
	if len(proof) <= ipa.ProofSize {
		t.Fatalf("expected path-bound proof larger than primitive size %d, got %d", ipa.ProofSize, len(proof))
	}

	primitiveProof, err := commitment.UnwrapPathProof("item", proof)
	if err != nil {
		t.Fatalf("UnwrapPathProof failed: %v", err)
	}
	ok, err := scheme.VerifyIndex(root, 0, target, primitiveProof)
	if err != nil {
		t.Fatalf("VerifyIndex failed: %v", err)
	}
	if !ok {
		t.Fatal("expected unwrapped primitive proof to verify")
	}

	ok, err = scheme.Verify(root, "item", target, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !ok {
		t.Fatal("expected proof to verify")
	}
}

func TestIPALegacySchemeRejectsWrongPath(t *testing.T) {
	scheme, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	target := newIndexedPayloadCID([]byte("value"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{"item": target})

	root, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	_, proof, err := scheme.Prove(root, arcs, "item")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	ok, err := scheme.Verify(root, "other", target, proof)
	if err == nil && ok {
		t.Fatal("verification should fail when the requested path differs from the proved path")
	}
}

func TestIPALegacySchemeBatchUpdateRestartSafe(t *testing.T) {
	first, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	oldA := newIndexedPayloadCID([]byte("a-old"))
	oldB := newIndexedPayloadCID([]byte("b-old"))
	arcs := arcset.NewSetFrom(map[string]cid.Cid{
		"a": oldA,
		"b": oldB,
	})

	root, err := first.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	second, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	newA := newIndexedPayloadCID([]byte("a-new"))
	updates := map[string]struct {
		Old cid.Cid
		New cid.Cid
	}{
		"a": {Old: oldA, New: newA},
	}

	newRoot, err := second.BatchUpdate(root, arcs, updates)
	if err != nil {
		t.Fatalf("BatchUpdate failed after restart: %v", err)
	}

	updatedArcs := arcset.NewSetFrom(map[string]cid.Cid{
		"a": newA,
		"b": oldB,
	})

	third, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	freshRoot, err := third.Commit(updatedArcs)
	if err != nil {
		t.Fatalf("Commit on updated arcs failed: %v", err)
	}
	if !freshRoot.Equals(newRoot) {
		t.Fatalf("batch update root does not match recomputed root")
	}

	values := []cid.Cid{newA, oldB}
	proved, proof, err := third.ProveIndex(newRoot, values, 0)
	if err != nil {
		t.Fatalf("ProveIndex on updated root failed: %v", err)
	}
	if !proved.Equals(newA) {
		t.Fatalf("unexpected proved value %s", proved)
	}

	ok, err := third.VerifyIndex(newRoot, 0, newA, proof)
	if err != nil {
		t.Fatalf("VerifyIndex on updated root failed: %v", err)
	}
	if !ok {
		t.Fatal("expected updated primitive proof to verify")
	}
}
