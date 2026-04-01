package malt_test

import (
	"testing"

	"github.com/dewebprotocol/malt/arcset"
	"github.com/dewebprotocol/malt/eat/simple"
	"github.com/dewebprotocol/malt/sce"
	"github.com/dewebprotocol/malt/sce/commitment/kzg"
	malt "github.com/dewebprotocol/malt/malt"
	"github.com/dewebprotocol/malt/key"
)

func TestStructureBasic(t *testing.T) {
	// Create components
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create arc set
	arcs := arcset.NewMap()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("link1", k1)
	arcs.Add("link2", k2)

	// Create structure
	structure, err := malt.NewStructure(arcs, e, s)
	if err != nil {
		t.Fatalf("NewStructure failed: %v", err)
	}

	// Check root
	if structure.Root() == nil {
		t.Error("Root should not be nil")
	}

	// Resolve link1
	resolved, proof, err := structure.Resolve("link1")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if !resolved.Equals(k1) {
		t.Error("Resolved key should equal k1")
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
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create initial structure
	arcs := arcset.NewMap()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("link", k1)

	structure, err := malt.NewStructure(arcs, e, s)
	if err != nil {
		t.Fatalf("NewStructure failed: %v", err)
	}

	// Update arc
	k2, _ := key.NewPayloadCID([]byte("target2"))
	newStructure, err := structure.Update("link", k2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Note: SimpleEAT is not versioned, so the behavior depends on EAT implementation
	// For SimpleEAT, the update creates a new root but the old root's EAT entry is not preserved
	// This test verifies that the new structure works correctly

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