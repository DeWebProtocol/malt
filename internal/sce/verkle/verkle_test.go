package verkle_test

import (
	"testing"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/verkle"
	"github.com/dewebprotocol/malt/key"
)

func TestVerkleCommitment(t *testing.T) {
	v, err := verkle.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	// Create arc set
	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	// Commit
	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if root == nil {
		t.Fatal("Root should not be nil")
	}

	if root.Kind() != key.KeyKindStructureRoot {
		t.Errorf("Expected StructureRoot, got %v", root.Kind())
	}

	// Prove
	target, proof, err := v.Prove(root, arcs, "a")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	if !target.Equals(k1) {
		t.Error("Target should equal k1")
	}

	if len(proof) == 0 {
		t.Error("Proof should not be empty")
	}

	// Verify
	valid, err := v.Verify(root, "a", k1, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestVerkleCommitmentUpdate(t *testing.T) {
	v, err := verkle.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	// Create initial structure
	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("link", k1)

	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Update
	k2, _ := key.NewPayloadCID([]byte("target2"))
	newRoot, err := v.Update(root, arcs, "link", k1, k2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

func TestVerkleBatchUpdate(t *testing.T) {
	v, err := verkle.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	// Create initial structure
	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Batch update
	k3, _ := key.NewPayloadCID([]byte("target3"))
	k4, _ := key.NewPayloadCID([]byte("target4"))
	updates := map[string]struct {
		Old key.Key
		New key.Key
	}{
		"a": {Old: k1, New: k3},
		"b": {Old: k2, New: k4},
	}

	newRoot, err := v.BatchUpdate(root, arcs, updates)
	if err != nil {
		t.Fatalf("BatchUpdate failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

func TestVerkleEmptyArcSet(t *testing.T) {
	v, err := verkle.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	// Empty arc set
	arcs := sce.NewMapArcSetView()

	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if root == nil {
		t.Fatal("Root should not be nil for empty arc set")
	}
}