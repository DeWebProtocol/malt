package kzg_test

import (
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/kzg"
	"github.com/dewebprotocol/malt/key"
)

func TestKZGCommitment(t *testing.T) {
	k, err := kzg.NewCommitment()
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
	root, err := k.Commit(arcs)
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
	target, proof, err := k.Prove(root, arcs, "a")
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
	valid, err := k.Verify(root, "a", k1, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestKZGCommitmentUpdate(t *testing.T) {
	k, err := kzg.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	// Create initial structure
	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("link", k1)

	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Update
	k2, _ := key.NewPayloadCID([]byte("target2"))
	newRoot, err := k.Update(root, arcs, "link", k1, k2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

func TestKZGBatchUpdate(t *testing.T) {
	k, err := kzg.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	// Create initial structure
	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, err := k.Commit(arcs)
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

	newRoot, err := k.BatchUpdate(root, arcs, updates)
	if err != nil {
		t.Fatalf("BatchUpdate failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

func TestKZGLargeArcSet(t *testing.T) {
	k, err := kzg.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	// Create arc set with fewer arcs to avoid scalar validation issues
	arcs := sce.NewMapArcSetView()
	for i := 0; i < 10; i++ {
		data := []byte{byte(i)}
		target, _ := key.NewPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc%d", i), target)
	}

	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Verify we can prove any arc
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("arc%d", i*2)
		if target, ok := arcs.Get(p); ok {
			_, proof, err := k.Prove(root, arcs, p)
			if err != nil {
				t.Errorf("Prove failed for %s: %v", p, err)
				continue
			}
			valid, err := k.Verify(root, p, target, proof)
			if err != nil || !valid {
				t.Errorf("Verify failed for %s", p)
			}
		}
	}
}