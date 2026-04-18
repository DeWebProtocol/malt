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
		t.Fatalf("expected wrapped legacy proof larger than primitive size %d, got %d", ipa.ProofSize, len(proof))
	}

	primitiveProof, err := commitment.UnwrapLegacyPathProof("item", proof)
	if err != nil {
		t.Fatalf("UnwrapLegacyPathProof failed: %v", err)
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
