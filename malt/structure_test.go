package malt_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvstore_memory "github.com/dewebprotocol/malt/core/types/kvstore/memory"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	malt "github.com/dewebprotocol/malt/malt"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// newPayloadCID creates a CID from data for testing.
func newPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

// newTestEAT creates a new EAT for testing.
func newTestEAT() *overwrite.EAT {
	kv := kvstore_memory.New()
	e, err := overwrite.NewEAT(kv, "test-graph")
	if err != nil {
		panic(err)
	}
	return e
}

func TestStructureBasic(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create arc set
	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Set("link1", k1)
	arcs.Set("link2", k2)

	// Create structure
	structure, err := malt.NewStructure(arcs, e, s)
	if err != nil {
		t.Fatalf("NewStructure failed: %v", err)
	}

	// Check root
	if !structure.Root().Defined() {
		t.Error("Root should be defined")
	}

	// Resolve link1
	resolved, proof, err := structure.Resolve("link1")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if !resolved.Equals(k1) {
		t.Error("Resolved CID should equal k1")
	}

	if proof == nil || len(proof) == 0 {
		t.Error("Proof should not be empty")
	}

	// Verify
	valid, err := structure.Verify("link1", k1, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestStructureUpdate(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create initial structure
	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Set("link", k1)

	structure, err := malt.NewStructure(arcs, e, s)
	if err != nil {
		t.Fatalf("NewStructure failed: %v", err)
	}

	// Update arc
	k2, _ := newPayloadCID([]byte("target2"))
	newStructure, err := structure.Update("link", k2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// New structure should resolve to new value
	resolved, proof, err := newStructure.Resolve("link")
	if err != nil {
		t.Fatalf("Resolve new structure failed: %v", err)
	}
	if !resolved.Equals(k2) {
		t.Error("New structure should have k2")
	}

	// Verify new structure
	valid, err := newStructure.Verify("link", k2, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Error("Proof should be valid for new structure")
	}

	// New root should be different from old root
	if newStructure.Root().Equals(structure.Root()) {
		t.Error("New root should be different from old root")
	}
}