package kzg_test

import (
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/kzg"
	"github.com/dewebprotocol/malt/key"
)

// === Basic Functionality Tests ===

func TestKZGCommitment(t *testing.T) {
	k, err := kzg.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

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

	if len(root.Bytes()) != 48 {
		t.Errorf("Expected 48 bytes for KZG commitment, got %d", len(root.Bytes()))
	}
}

func TestKZGProveAndVerify(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("my-target"))
	arcs.Add("my-arc", target)

	root, _ := k.Commit(arcs)

	// Prove
	provedTarget, proof, err := k.Prove(root, arcs, "my-arc")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	if !provedTarget.Equals(target) {
		t.Error("Proved target should match original")
	}

	if len(proof) != 84 {
		t.Errorf("Expected proof size 84, got %d", len(proof))
	}

	// Verify
	valid, err := k.Verify(root, "my-arc", target, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestKZGUpdate(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	oldTarget, _ := key.NewPayloadCID([]byte("old"))
	arcs.Add("link", oldTarget)

	root, _ := k.Commit(arcs)

	newTarget, _ := key.NewPayloadCID([]byte("new"))
	newRoot, err := k.Update(root, arcs, "link", oldTarget, newTarget)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}

	// Verify new root can prove the value
	// Note: We need to create a new arc set with the updated value for proving
	updatedArcs := sce.NewMapArcSetView()
	updatedArcs.Add("link", newTarget)

	proved, proof, err := k.Prove(newRoot, updatedArcs, "link")
	if err != nil {
		t.Fatalf("Prove after update failed: %v", err)
	}

	if !proved.Equals(newTarget) {
		t.Error("Proved value should be new target")
	}

	valid, _ := k.Verify(newRoot, "link", newTarget, proof)
	if !valid {
		t.Error("Proof for new value should be valid")
	}
}

func TestKZGBatchUpdate(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	k3, _ := key.NewPayloadCID([]byte("target3"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)
	arcs.Add("c", k3)

	root, _ := k.Commit(arcs)

	newK1, _ := key.NewPayloadCID([]byte("new1"))
	newK2, _ := key.NewPayloadCID([]byte("new2"))

	updates := map[string]struct {
		Old key.Key
		New key.Key
	}{
		"a": {Old: k1, New: newK1},
		"b": {Old: k2, New: newK2},
	}

	newRoot, err := k.BatchUpdate(root, arcs, updates)
	if err != nil {
		t.Fatalf("BatchUpdate failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

// === Error Cases ===

func TestKZGCommitNilArcSet(t *testing.T) {
	k, _ := kzg.NewCommitment()

	_, err := k.Commit(nil)
	if err == nil {
		t.Error("Should error on nil arc set")
	}
}

func TestKZGCommitEmptyArcSet(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Should handle empty arc set: %v", err)
	}

	// Empty commitment should still work
	if root == nil {
		t.Error("Should return a root for empty arc set")
	}
}

func TestKZGProveNonExistentPath(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("exists", target)

	root, _ := k.Commit(arcs)

	_, _, err := k.Prove(root, arcs, "non-existent")
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestKZGProveWrongRootType(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("a", target)

	// Create a PayloadCID instead of StructureRoot
	wrongRoot, _ := key.NewPayloadCID([]byte("not-a-root"))

	_, _, err := k.Prove(wrongRoot, arcs, "a")
	if err == nil {
		t.Error("Should error on wrong root type")
	}
}

func TestKZGVerifyWrongProof(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("a", target)

	root, _ := k.Commit(arcs)

	// Create wrong proof
	wrongProof := make([]byte, 84)
	for i := range wrongProof {
		wrongProof[i] = byte(i)
	}

	valid, err := k.Verify(root, "a", target, wrongProof)
	if err != nil {
		// Invalid proof should return false, not error
		t.Fatalf("Verify should not error: %v", err)
	}

	if valid {
		t.Error("Wrong proof should be invalid")
	}
}

func TestKZGVerifyShortProof(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("a", target)

	root, _ := k.Commit(arcs)

	shortProof := []byte{1, 2, 3}

	_, err := k.Verify(root, "a", target, shortProof)
	if err == nil {
		t.Error("Should error on short proof")
	}
}

func TestKZGUpdateNonExistentPath(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("a", target)

	root, _ := k.Commit(arcs)

	oldKey, _ := key.NewPayloadCID([]byte("old"))
	newKey, _ := key.NewPayloadCID([]byte("new"))

	_, err := k.Update(root, arcs, "non-existent", oldKey, newKey)
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

// === Edge Cases ===

func TestKZGLargeArcSet(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	for i := 0; i < 1000; i++ {
		data := []byte{byte(i % 256), byte((i / 256) % 256)}
		target, _ := key.NewPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc_%d", i), target)
	}

	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed for large arc set: %v", err)
	}

	// Prove a few random arcs
	for _, i := range []int{0, 500, 999} {
		path := fmt.Sprintf("arc_%d", i)
		target, ok := arcs.Get(path)
		if !ok {
			t.Fatalf("Arc %s not found", path)
		}

		_, proof, err := k.Prove(root, arcs, path)
		if err != nil {
			t.Errorf("Prove failed for %s: %v", path, err)
			continue
		}

		valid, _ := k.Verify(root, path, target, proof)
		if !valid {
			t.Errorf("Proof invalid for %s", path)
		}
	}
}

func TestKZGArcSetExceedsLimit(t *testing.T) {
	k, _ := kzg.NewCommitment()

	arcs := sce.NewMapArcSetView()
	// KZG has max 4096 arcs
	for i := 0; i < 5000; i++ {
		data := []byte{byte(i % 256)}
		target, _ := key.NewPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc_%d", i), target)
	}

	_, err := k.Commit(arcs)
	if err == nil {
		t.Error("Should error when arc set exceeds limit")
	}
}

func TestKZGMultipleCommits(t *testing.T) {
	k, _ := kzg.NewCommitment()

	// First commit
	arcs1 := sce.NewMapArcSetView()
	target1, _ := key.NewPayloadCID([]byte("data1"))
	arcs1.Add("a", target1)
	root1, _ := k.Commit(arcs1)

	// Second commit
	arcs2 := sce.NewMapArcSetView()
	target2, _ := key.NewPayloadCID([]byte("data2"))
	arcs2.Add("b", target2)
	root2, _ := k.Commit(arcs2)

	// Both should be independently provable
	_, proof1, err := k.Prove(root1, arcs1, "a")
	if err != nil {
		t.Errorf("Prove root1 failed: %v", err)
	}

	_, proof2, err := k.Prove(root2, arcs2, "b")
	if err != nil {
		t.Errorf("Prove root2 failed: %v", err)
	}

	valid1, _ := k.Verify(root1, "a", target1, proof1)
	valid2, _ := k.Verify(root2, "b", target2, proof2)

	if !valid1 || !valid2 {
		t.Error("Both proofs should be valid")
	}
}

// === Options Tests ===

func TestKZGWithOptions(t *testing.T) {
	k, err := kzg.NewCommitment(
		kzg.WithVectorSize(4096),
		kzg.WithCacheSize(100),
	)
	if err != nil {
		t.Fatalf("NewCommitment with options failed: %v", err)
	}

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("test"))
	arcs.Add("a", target)

	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if root == nil {
		t.Error("Root should not be nil")
	}
}