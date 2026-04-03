package verkle_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/sce/commitment/verkle"
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

func TestVerkleCommitment(t *testing.T) {
	v, err := verkle.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Set("a", k1)
	arcs.Set("b", k2)

	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if !root.Defined() {
		t.Fatal("Root should be defined")
	}

	if !codec.IsMaltCid(root) {
		t.Errorf("Expected MALT commitment CID, got codec=%x", root.Prefix().Codec)
	}

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

	valid, err := v.Verify(root, "a", k1, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestVerkleCommitmentUpdate(t *testing.T) {
	v, err := verkle.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Set("link", k1)

	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	k2, _ := newPayloadCID([]byte("target2"))
	newRoot, err := v.Update(root, arcs, "link", k1, k2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

func TestVerkleBatchUpdate(t *testing.T) {
	v, err := verkle.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Set("a", k1)
	arcs.Set("b", k2)

	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	k3, _ := newPayloadCID([]byte("target3"))
	k4, _ := newPayloadCID([]byte("target4"))
	updates := map[string]struct {
		Old cid.Cid
		New cid.Cid
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

// === Aggregation Proof Tests ===

func TestVerkleProveBatch(t *testing.T) {
	v, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Set("a", k1)
	arcs.Set("b", k2)

	root, _ := v.Commit(arcs)

	paths := []string{"a", "b"}
	proofs, err := v.ProveBatch(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveBatch failed: %v", err)
	}

	if len(proofs) != 2 {
		t.Errorf("Expected 2 proofs, got %d", len(proofs))
	}
}

func TestVerkleVerifyBatch(t *testing.T) {
	v, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Set("a", k1)
	arcs.Set("b", k2)

	root, _ := v.Commit(arcs)

	paths := []string{"a", "b"}
	proofs, _ := v.ProveBatch(root, arcs, paths)

	valid, err := v.VerifyBatch(root, proofs)
	if err != nil {
		t.Fatalf("VerifyBatch failed: %v", err)
	}

	if !valid {
		t.Error("Batch proofs should be valid")
	}
}

func TestVerkleProveAggregate(t *testing.T) {
	v, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Set("a", k1)
	arcs.Set("b", k2)

	root, _ := v.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, err := v.ProveAggregate(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveAggregate failed: %v", err)
	}

	if len(aggProof.Paths) != 2 {
		t.Errorf("Expected 2 paths, got %d", len(aggProof.Paths))
	}

	if len(aggProof.Targets) != 2 {
		t.Errorf("Expected 2 targets, got %d", len(aggProof.Targets))
	}

	if len(aggProof.ProofData) != 62 { // 2 * 31 bytes
		t.Errorf("Expected proof data size 62, got %d", len(aggProof.ProofData))
	}
}

func TestVerkleVerifyAggregate(t *testing.T) {
	v, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs.Set("a", k1)
	arcs.Set("b", k2)

	root, _ := v.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, _ := v.ProveAggregate(root, arcs, paths)

	valid, err := v.VerifyAggregate(root, aggProof)
	if err != nil {
		t.Fatalf("VerifyAggregate failed: %v", err)
	}

	if !valid {
		t.Error("Aggregated proof should be valid")
	}
}

// === Error Cases ===

func TestVerkleEmptyArcSet(t *testing.T) {
	v, err := verkle.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	arcs := arcset.NewMap()

	root, err := v.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if !root.Defined() {
		t.Fatal("Root should be defined for empty arc set")
	}
}

func TestVerkleProveNonExistentPath(t *testing.T) {
	v, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Set("a", k1)

	root, _ := v.Commit(arcs)

	_, _, err := v.Prove(root, arcs, "nonexistent")
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestVerkleProveAggregateEmptyPaths(t *testing.T) {
	v, _ := verkle.NewScheme()

	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Set("a", k1)

	root, _ := v.Commit(arcs)

	_, err := v.ProveAggregate(root, arcs, []string{})
	if err == nil {
		t.Error("Should error on empty paths")
	}
}

func TestVerkleCommitNilArcSet(t *testing.T) {
	v, _ := verkle.NewScheme()

	_, err := v.Commit(nil)
	if err == nil {
		t.Error("Should error on nil arc set")
	}
}