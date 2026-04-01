package ipa_test

import (
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/ipa"
	"github.com/dewebprotocol/malt/key"
)

// === Basic Functionality Tests ===

func TestIPACommitment(t *testing.T) {
	i, err := ipa.NewCommitment()
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, err := i.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if root == nil {
		t.Fatal("Root should not be nil")
	}

	if root.Kind() != key.KeyKindStructureRoot {
		t.Errorf("Expected StructureRoot, got %v", root.Kind())
	}
}

func TestIPAProveAndVerify(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("my-target"))
	arcs.Add("my-arc", target)

	root, _ := i.Commit(arcs)

	provedTarget, proof, err := i.Prove(root, arcs, "my-arc")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	if !provedTarget.Equals(target) {
		t.Error("Proved target should match original")
	}

	if len(proof) == 0 {
		t.Error("Proof should not be empty")
	}

	valid, err := i.Verify(root, "my-arc", target, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestIPAUpdate(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	oldTarget, _ := key.NewPayloadCID([]byte("old"))
	arcs.Add("link", oldTarget)

	root, _ := i.Commit(arcs)

	newTarget, _ := key.NewPayloadCID([]byte("new"))
	newRoot, err := i.Update(root, arcs, "link", oldTarget, newTarget)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

func TestIPABatchUpdate(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	k3, _ := key.NewPayloadCID([]byte("target3"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)
	arcs.Add("c", k3)

	root, _ := i.Commit(arcs)

	newK1, _ := key.NewPayloadCID([]byte("new1"))
	newK2, _ := key.NewPayloadCID([]byte("new2"))

	updates := map[string]struct {
		Old key.Key
		New key.Key
	}{
		"a": {Old: k1, New: newK1},
		"b": {Old: k2, New: newK2},
	}

	newRoot, err := i.BatchUpdate(root, arcs, updates)
	if err != nil {
		t.Fatalf("BatchUpdate failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

// === Aggregation Proof Tests ===

func TestIPAProveBatch(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, _ := i.Commit(arcs)

	paths := []string{"a", "b"}
	proofs, err := i.ProveBatch(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveBatch failed: %v", err)
	}

	if len(proofs) != 2 {
		t.Errorf("Expected 2 proofs, got %d", len(proofs))
	}
}

func TestIPAVerifyBatch(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, _ := i.Commit(arcs)

	paths := []string{"a", "b"}
	proofs, _ := i.ProveBatch(root, arcs, paths)

	valid, err := i.VerifyBatch(root, proofs)
	if err != nil {
		t.Fatalf("VerifyBatch failed: %v", err)
	}

	if !valid {
		t.Error("Batch proofs should be valid")
	}
}

func TestIPAProveAggregate(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, _ := i.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, err := i.ProveAggregate(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveAggregate failed: %v", err)
	}

	if len(aggProof.Paths) != 2 {
		t.Errorf("Expected 2 paths, got %d", len(aggProof.Paths))
	}

	if len(aggProof.Targets) != 2 {
		t.Errorf("Expected 2 targets, got %d", len(aggProof.Targets))
	}
}

func TestIPAVerifyAggregate(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, _ := i.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, _ := i.ProveAggregate(root, arcs, paths)

	valid, err := i.VerifyAggregate(root, aggProof)
	if err != nil {
		t.Fatalf("VerifyAggregate failed: %v", err)
	}

	if !valid {
		t.Error("Aggregated proof should be valid")
	}
}

func TestIPAProveBatchWithMultiplePaths(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	for j := 0; j < 10; j++ {
		k, _ := key.NewPayloadCID([]byte{byte(j)})
		arcs.Add(fmt.Sprintf("item_%d", j), k)
	}

	root, _ := i.Commit(arcs)

	paths := []string{"item_0", "item_5", "item_9"}
	proofs, err := i.ProveBatch(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveBatch failed: %v", err)
	}

	if len(proofs) != 3 {
		t.Errorf("Expected 3 proofs, got %d", len(proofs))
	}

	valid, _ := i.VerifyBatch(root, proofs)
	if !valid {
		t.Error("Batch proofs should be valid")
	}
}

// === Error Cases ===

func TestIPACommitNilArcSet(t *testing.T) {
	i, _ := ipa.NewCommitment()

	_, err := i.Commit(nil)
	if err == nil {
		t.Error("Should error on nil arc set")
	}
}

func TestIPACommitEmptyArcSet(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	root, err := i.Commit(arcs)
	if err != nil {
		t.Fatalf("Should handle empty arc set: %v", err)
	}

	if root == nil {
		t.Error("Should return a root for empty arc set")
	}
}

func TestIPAProveNonExistentPath(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("exists", target)

	root, _ := i.Commit(arcs)

	_, _, err := i.Prove(root, arcs, "non-existent")
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestIPAProveWrongRootType(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("a", target)

	wrongRoot, _ := key.NewPayloadCID([]byte("not-a-root"))

	_, _, err := i.Prove(wrongRoot, arcs, "a")
	if err == nil {
		t.Error("Should error on wrong root type")
	}
}

func TestIPAUpdateNonExistentPath(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	target, _ := key.NewPayloadCID([]byte("data"))
	arcs.Add("a", target)

	root, _ := i.Commit(arcs)

	oldKey, _ := key.NewPayloadCID([]byte("old"))
	newKey, _ := key.NewPayloadCID([]byte("new"))

	_, err := i.Update(root, arcs, "non-existent", oldKey, newKey)
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestIPAProveBatchEmptyPaths(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	root, _ := i.Commit(arcs)

	_, err := i.ProveBatch(root, arcs, []string{})
	if err == nil {
		t.Error("Should error on empty paths")
	}
}

func TestIPAProveAggregateEmptyPaths(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	root, _ := i.Commit(arcs)

	_, err := i.ProveAggregate(root, arcs, []string{})
	if err == nil {
		t.Error("Should error on empty paths")
	}
}

func TestIPAProveBatchNonExistentPath(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	root, _ := i.Commit(arcs)

	_, err := i.ProveBatch(root, arcs, []string{"nonexistent"})
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

// === Edge Cases ===

func TestIPALargeArcSet(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	for j := 0; j < 200; j++ {
		data := []byte{byte(j % 256), byte((j / 256) % 256)}
		target, _ := key.NewPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc_%d", j), target)
	}

	root, err := i.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed for large arc set: %v", err)
	}

	for _, j := range []int{0, 100, 199} {
		path := fmt.Sprintf("arc_%d", j)
		target, ok := arcs.Get(path)
		if !ok {
			t.Fatalf("Arc %s not found", path)
		}

		_, proof, err := i.Prove(root, arcs, path)
		if err != nil {
			t.Errorf("Prove failed for %s: %v", path, err)
			continue
		}

		valid, _ := i.Verify(root, path, target, proof)
		if !valid {
			t.Errorf("Proof invalid for %s", path)
		}
	}
}

func TestIPAArcSetExceedsLimit(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs := sce.NewMapArcSetView()
	// IPA has max 256 arcs
	for j := 0; j < 300; j++ {
		data := []byte{byte(j % 256)}
		target, _ := key.NewPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc_%d", j), target)
	}

	_, err := i.Commit(arcs)
	if err == nil {
		t.Error("Should error when arc set exceeds limit")
	}
}

func TestIPAMultipleCommits(t *testing.T) {
	i, _ := ipa.NewCommitment()

	arcs1 := sce.NewMapArcSetView()
	target1, _ := key.NewPayloadCID([]byte("data1"))
	arcs1.Add("a", target1)
	root1, _ := i.Commit(arcs1)

	arcs2 := sce.NewMapArcSetView()
	target2, _ := key.NewPayloadCID([]byte("data2"))
	arcs2.Add("b", target2)
	root2, _ := i.Commit(arcs2)

	_, proof1, err := i.Prove(root1, arcs1, "a")
	if err != nil {
		t.Errorf("Prove root1 failed: %v", err)
	}

	_, proof2, err := i.Prove(root2, arcs2, "b")
	if err != nil {
		t.Errorf("Prove root2 failed: %v", err)
	}

	valid1, _ := i.Verify(root1, "a", target1, proof1)
	valid2, _ := i.Verify(root2, "b", target2, proof2)

	if !valid1 || !valid2 {
		t.Error("Both proofs should be valid")
	}
}