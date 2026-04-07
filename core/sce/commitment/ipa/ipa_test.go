package ipa_test

import (
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
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

func TestIPACommitment(t *testing.T) {
	s, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1, "b": k2})

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if !root.Defined() {
		t.Fatal("Root should be defined")
	}

	if !codec.IsMaltCid(root) {
		t.Errorf("Expected MALT commitment CID, got codec=%x", root.Prefix().Codec)
	}
}

func TestIPAProveAndVerify(t *testing.T) {
	s, _ := ipa.NewScheme()

	target, _ := newPayloadCID([]byte("my-target"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"my-arc": target})

	root, _ := s.Commit(arcs)

	provedTarget, proof, err := s.Prove(root, arcs, "my-arc")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	if !provedTarget.Equals(target) {
		t.Error("Proved target should match original")
	}

	if len(proof) == 0 {
		t.Error("Proof should not be empty")
	}

	valid, err := s.Verify(root, "my-arc", target, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestIPAUpdate(t *testing.T) {
	s, _ := ipa.NewScheme()

	oldTarget, _ := newPayloadCID([]byte("old"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"link": oldTarget})

	root, _ := s.Commit(arcs)

	newTarget, _ := newPayloadCID([]byte("new"))
	newRoot, err := s.Update(root, arcs, "link", oldTarget, newTarget)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

func TestIPABatchUpdate(t *testing.T) {
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1, "b": k2, "c": k3})

	root, _ := s.Commit(arcs)

	newK1, _ := newPayloadCID([]byte("new1"))
	newK2, _ := newPayloadCID([]byte("new2"))

	updates := map[string]struct {
		Old cid.Cid
		New cid.Cid
	}{
		"a": {Old: k1, New: newK1},
		"b": {Old: k2, New: newK2},
	}

	newRoot, err := s.BatchUpdate(root, arcs, updates)
	if err != nil {
		t.Fatalf("BatchUpdate failed: %v", err)
	}

	if newRoot.Equals(root) {
		t.Error("New root should differ from old root")
	}
}

// === Aggregation Proof Tests ===

func TestIPAProveBatch(t *testing.T) {
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1, "b": k2})

	root, _ := s.Commit(arcs)

	paths := []string{"a", "b"}
	proofs, err := s.ProveBatch(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveBatch failed: %v", err)
	}

	if len(proofs) != 2 {
		t.Errorf("Expected 2 proofs, got %d", len(proofs))
	}
}

func TestIPAVerifyBatch(t *testing.T) {
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1, "b": k2})

	root, _ := s.Commit(arcs)

	paths := []string{"a", "b"}
	proofs, _ := s.ProveBatch(root, arcs, paths)

	valid, err := s.VerifyBatch(root, proofs)
	if err != nil {
		t.Fatalf("VerifyBatch failed: %v", err)
	}

	if !valid {
		t.Error("Batch proofs should be valid")
	}
}

func TestIPAProveAggregate(t *testing.T) {
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1, "b": k2})

	root, _ := s.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, err := s.ProveAggregate(root, arcs, paths)
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
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1, "b": k2})

	root, _ := s.Commit(arcs)

	paths := []string{"a", "b"}
	aggProof, _ := s.ProveAggregate(root, arcs, paths)

	valid, err := s.VerifyAggregate(root, aggProof)
	if err != nil {
		t.Fatalf("VerifyAggregate failed: %v", err)
	}

	if !valid {
		t.Error("Aggregated proof should be valid")
	}
}

func TestIPAProveBatchWithMultiplePaths(t *testing.T) {
	s, _ := ipa.NewScheme()

	arcsMap := make(map[string]cid.Cid)
	for j := 0; j < 10; j++ {
		k, _ := newPayloadCID([]byte{byte(j)})
		arcsMap[fmt.Sprintf("item_%d", j)] = k
	}
	arcs := arcset.NewMapFrom(arcsMap)

	root, _ := s.Commit(arcs)

	paths := []string{"item_0", "item_5", "item_9"}
	proofs, err := s.ProveBatch(root, arcs, paths)
	if err != nil {
		t.Fatalf("ProveBatch failed: %v", err)
	}

	if len(proofs) != 3 {
		t.Errorf("Expected 3 proofs, got %d", len(proofs))
	}

	valid, _ := s.VerifyBatch(root, proofs)
	if !valid {
		t.Error("Batch proofs should be valid")
	}
}

// === Error Cases ===

func TestIPACommitNilArcSet(t *testing.T) {
	s, _ := ipa.NewScheme()

	_, err := s.Commit(nil)
	if err == nil {
		t.Error("Should error on nil arc set")
	}
}

func TestIPACommitEmptyArcSet(t *testing.T) {
	s, _ := ipa.NewScheme()

	arcs := arcset.NewMap()
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Should handle empty arc set: %v", err)
	}

	if !root.Defined() {
		t.Error("Should return a root for empty arc set")
	}
}

func TestIPAProveNonExistentPath(t *testing.T) {
	s, _ := ipa.NewScheme()

	target, _ := newPayloadCID([]byte("data"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"exists": target})

	root, _ := s.Commit(arcs)

	_, _, err := s.Prove(root, arcs, "non-existent")
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestIPAProveWrongRootType(t *testing.T) {
	s, _ := ipa.NewScheme()

	target, _ := newPayloadCID([]byte("data"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": target})

	wrongRoot, _ := newPayloadCID([]byte("not-a-root"))

	_, _, err := s.Prove(wrongRoot, arcs, "a")
	if err == nil {
		t.Error("Should error on wrong root type")
	}
}

func TestIPAUpdateNonExistentPath(t *testing.T) {
	s, _ := ipa.NewScheme()

	target, _ := newPayloadCID([]byte("data"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": target})

	root, _ := s.Commit(arcs)

	oldKey, _ := newPayloadCID([]byte("old"))
	newKey, _ := newPayloadCID([]byte("new"))

	_, err := s.Update(root, arcs, "non-existent", oldKey, newKey)
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

func TestIPAProveBatchEmptyPaths(t *testing.T) {
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1})

	root, _ := s.Commit(arcs)

	_, err := s.ProveBatch(root, arcs, []string{})
	if err == nil {
		t.Error("Should error on empty paths")
	}
}

func TestIPAProveAggregateEmptyPaths(t *testing.T) {
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1})

	root, _ := s.Commit(arcs)

	_, err := s.ProveAggregate(root, arcs, []string{})
	if err == nil {
		t.Error("Should error on empty paths")
	}
}

func TestIPAProveBatchNonExistentPath(t *testing.T) {
	s, _ := ipa.NewScheme()

	k1, _ := newPayloadCID([]byte("target1"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{"a": k1})

	root, _ := s.Commit(arcs)

	_, err := s.ProveBatch(root, arcs, []string{"nonexistent"})
	if err == nil {
		t.Error("Should error on non-existent path")
	}
}

// === Edge Cases ===

func TestIPALargeArcSet(t *testing.T) {
	s, _ := ipa.NewScheme()

	arcsMap := make(map[string]cid.Cid)
	for j := 0; j < 200; j++ {
		data := []byte{byte(j % 256), byte((j / 256) % 256)}
		target, _ := newPayloadCID(data)
		arcsMap[fmt.Sprintf("arc_%d", j)] = target
	}
	arcs := arcset.NewMapFrom(arcsMap)

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed for large arc set: %v", err)
	}

	for _, j := range []int{0, 100, 199} {
		path := fmt.Sprintf("arc_%d", j)
		target, ok := arcs.Get(path)
		if !ok {
			t.Fatalf("Arc %s not found", path)
		}

		_, proof, err := s.Prove(root, arcs, path)
		if err != nil {
			t.Errorf("Prove failed for %s: %v", path, err)
			continue
		}

		valid, _ := s.Verify(root, path, target, proof)
		if !valid {
			t.Errorf("Proof invalid for %s", path)
		}
	}
}

func TestIPAArcSetExceedsLimit(t *testing.T) {
	s, _ := ipa.NewScheme()

	arcsMap := make(map[string]cid.Cid)
	// IPA has max 256 arcs
	for j := 0; j < 300; j++ {
		data := []byte{byte(j % 256)}
		target, _ := newPayloadCID(data)
		arcsMap[fmt.Sprintf("arc_%d", j)] = target
	}
	arcs := arcset.NewMapFrom(arcsMap)

	_, err := s.Commit(arcs)
	if err == nil {
		t.Error("Should error when arc set exceeds limit")
	}
}

func TestIPAMultipleCommits(t *testing.T) {
	s, _ := ipa.NewScheme()

	target1, _ := newPayloadCID([]byte("data1"))
	arcs1 := arcset.NewMapFrom(map[string]cid.Cid{"a": target1})
	root1, _ := s.Commit(arcs1)

	target2, _ := newPayloadCID([]byte("data2"))
	arcs2 := arcset.NewMapFrom(map[string]cid.Cid{"b": target2})
	root2, _ := s.Commit(arcs2)

	_, proof1, err := s.Prove(root1, arcs1, "a")
	if err != nil {
		t.Errorf("Prove root1 failed: %v", err)
	}

	_, proof2, err := s.Prove(root2, arcs2, "b")
	if err != nil {
		t.Errorf("Prove root2 failed: %v", err)
	}

	valid1, _ := s.Verify(root1, "a", target1, proof1)
	valid2, _ := s.Verify(root2, "b", target2, proof2)

	if !valid1 || !valid2 {
		t.Error("Both proofs should be valid")
	}
}