package kzg_test

import (
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/eat/memory"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/types/arcset"
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

// === Basic Functionality Tests ===

func TestKZGCommitment(t *testing.T) {
	k, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if !root.Defined() {
		t.Fatal("Root should be defined")
	}

	if !codec.IsMaltCid(root) {
		t.Errorf("Expected MALT commitment CID, got codec=%x", root.Prefix().Codec)
	}

	commitment, err := codec.ExtractCommitment(root)
	if err != nil {
		t.Fatalf("ExtractCommitment failed: %v", err)
	}
	if len(commitment) != 48 {
		t.Errorf("Expected 48 bytes for KZG commitment, got %d", len(commitment))
	}
}

func TestKZGProveAndVerify(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	target, _ := newPayloadCID([]byte("my-target"))
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
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	oldTarget, _ := newPayloadCID([]byte("old"))
	arcs.Add("link", oldTarget)

	root, _ := k.Commit(arcs)

	newTarget, _ := newPayloadCID([]byte("new"))
	newRoot, err := k.Update(root, arcs, "link", oldTarget, newTarget)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}

	// Verify new root can prove the value
	updatedArcs := memory.NewView()
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
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)
	arcs.Add("c", k3)

	root, _ := k.Commit(arcs)

	newK1, _ := newPayloadCID([]byte("new1"))
	newK2, _ := newPayloadCID([]byte("new2"))

	updates := map[string]struct {
		Old cid.Cid
		New cid.Cid
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

// === Aggregation Proof Tests ===

func TestKZGProveBatch(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)
	arcs.Add("c", k3)

	root, _ := k.Commit(arcs)

	paths := []string{"a", "b", "c"}
	proofs, err := k.ProveBatch(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveBatch failed: %v", err)
	}

	if len(proofs) != 3 {
		t.Errorf("Expected 3 proofs, got %d", len(proofs))
	}

	for _, path := range paths {
		entry, ok := proofs[path]
		if !ok {
			t.Errorf("Missing proof for path %s", path)
			continue
		}

		if len(entry.Proof) != 84 {
			t.Errorf("Expected proof size 84 for path %s, got %d", path, len(entry.Proof))
		}
	}
}

func TestKZGVerifyBatch(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)
	arcs.Add("c", k3)

	root, _ := k.Commit(arcs)

	paths := []string{"a", "b", "c"}
	proofs, _ := k.ProveBatch(root, arcs, paths)

	valid, err := k.VerifyBatch(root, proofs)
	if err != nil {
		t.Fatalf("VerifyBatch failed: %v", err)
	}

	if !valid {
		t.Error("Batch proofs should be valid")
	}
}

func TestKZGVerifyBatchWithInvalidProof(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	root, _ := k.Commit(arcs)

	invalidProof := make([]byte, 84)
	for i := range invalidProof {
		invalidProof[i] = byte(i)
	}

	proofs := map[string]arcset.BatchProofEntry{
		"a": {
			Target: k1,
			Proof:  arcset.Proof(invalidProof),
		},
	}

	valid, _ := k.VerifyBatch(root, proofs)
	if valid {
		t.Error("Invalid batch proof should not be valid")
	}
}

func TestKZGProveAggregate(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, _ := k.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, err := k.ProveAggregate(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveAggregate failed: %v", err)
	}

	if len(aggProof.Paths) != 2 {
		t.Errorf("Expected 2 paths, got %d", len(aggProof.Paths))
	}

	if len(aggProof.Targets) != 2 {
		t.Errorf("Expected 2 targets, got %d", len(aggProof.Targets))
	}

	if len(aggProof.ProofData) != 160 { // 2 * 80 bytes (proof + claimedValue)
		t.Errorf("Expected proof data size 160, got %d", len(aggProof.ProofData))
	}
}

func TestKZGVerifyAggregate(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Add("a", k1)
	arcs.Add("b", k2)

	root, _ := k.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, _ := k.ProveAggregate(root, arcs, paths)

	valid, err := k.VerifyAggregate(root, aggProof)
	if err != nil {
		t.Fatalf("VerifyAggregate failed: %v", err)
	}

	if !valid {
		t.Error("Aggregated proof should be valid")
	}
}

// === Error Cases ===

func TestKZGCommitEmptyArcSet(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Should handle empty arc set: %v", err)
	}

	if !root.Defined() {
		t.Error("Should return a root for empty arc set")
	}
}

func TestKZGProveNonExistentPath(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	target, _ := newPayloadCID([]byte("data"))
	arcs.Add("exists", target)

	root, _ := k.Commit(arcs)

	_, _, err := k.Prove(root, arcs, "non-existent")
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestKZGVerifyWrongProof(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	target, _ := newPayloadCID([]byte("data"))
	arcs.Add("a", target)

	root, _ := k.Commit(arcs)

	wrongProof := make([]byte, 84)
	for i := range wrongProof {
		wrongProof[i] = byte(i)
	}

	valid, err := k.Verify(root, "a", target, wrongProof)
	if err != nil {
		t.Fatalf("Verify should not error: %v", err)
	}

	if valid {
		t.Error("Wrong proof should be invalid")
	}
}

func TestKZGVerifyShortProof(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	target, _ := newPayloadCID([]byte("data"))
	arcs.Add("a", target)

	root, _ := k.Commit(arcs)

	shortProof := []byte{1, 2, 3}

	_, err := k.Verify(root, "a", target, shortProof)
	if err == nil {
		t.Error("Should error on short proof")
	}
}

func TestKZGUpdateNonExistentPath(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	target, _ := newPayloadCID([]byte("data"))
	arcs.Add("a", target)

	root, _ := k.Commit(arcs)

	oldKey, _ := newPayloadCID([]byte("old"))
	newKey, _ := newPayloadCID([]byte("new"))

	_, err := k.Update(root, arcs, "non-existent", oldKey, newKey)
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestKZGProveBatchEmptyPaths(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	root, _ := k.Commit(arcs)

	_, err := k.ProveBatch(root, arcs, []string{})
	if err == nil {
		t.Error("Should error on empty paths")
	}
}

func TestKZGProveAggregateEmptyPaths(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	root, _ := k.Commit(arcs)

	_, err := k.ProveAggregate(root, arcs, []string{})
	if err == nil {
		t.Error("Should error on empty paths")
	}
}

func TestKZGProveBatchNonExistentPath(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	root, _ := k.Commit(arcs)

	_, err := k.ProveBatch(root, arcs, []string{"nonexistent"})
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

// === Edge Cases ===

func TestKZGLargeArcSet(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	for i := 0; i < 1000; i++ {
		data := []byte{byte(i % 256), byte((i / 256) % 256)}
		target, _ := newPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc_%d", i), target)
	}

	root, err := k.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed for large arc set: %v", err)
	}

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
	k, _ := kzg.NewScheme()

	arcs := memory.NewView()
	for i := 0; i < 5000; i++ {
		data := []byte{byte(i % 256)}
		target, _ := newPayloadCID(data)
		arcs.Add(fmt.Sprintf("arc_%d", i), target)
	}

	_, err := k.Commit(arcs)
	if err == nil {
		t.Error("Should error when arc set exceeds limit")
	}
}

func TestKZGMultipleCommits(t *testing.T) {
	k, _ := kzg.NewScheme()

	arcs1 := memory.NewView()
	target1, _ := newPayloadCID([]byte("data1"))
	arcs1.Add("a", target1)
	root1, _ := k.Commit(arcs1)

	arcs2 := memory.NewView()
	target2, _ := newPayloadCID([]byte("data2"))
	arcs2.Add("b", target2)
	root2, _ := k.Commit(arcs2)

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