// Package kzg_test provides tests for the KZG commitment implementation.
package kzg_test

import (
	"testing"

	"github.com/dewebprotocol/malt/pkg/commitment/kzg"
	"github.com/dewebprotocol/malt/pkg/types"
)

func TestKZGCommitment_Commit(t *testing.T) {
	// Note: This test uses the secure trusted setup which takes a few seconds to initialize
	scheme, err := kzg.New(nil)
	if err != nil {
		t.Fatalf("Failed to create KZG scheme: %v", err)
	}

	// Create test arc set
	target1, _ := types.NewCID([]byte("target1_data"))
	target2, _ := types.NewCID([]byte("target2_data"))

	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("path1", target1),
		types.NewArcPair("path2", target2),
	)

	// Generate commitment
	comm, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	if len(comm) == 0 {
		t.Error("Commitment should not be empty")
	}

	t.Logf("Generated commitment: %x", comm[:min(16, len(comm))])
}

func TestKZGCommitment_ProveVerify(t *testing.T) {
	scheme, err := kzg.New(nil)
	if err != nil {
		t.Fatalf("Failed to create KZG scheme: %v", err)
	}

	target1, _ := types.NewCID([]byte("target1_data"))
	target2, _ := types.NewCID([]byte("target2_data"))

	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("path1", target1),
		types.NewArcPair("path2", target2),
	)

	comm, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Generate proof for path1
	cid, proof, err := scheme.Prove(comm, arcs, "path1")
	if err != nil {
		t.Fatalf("Failed to prove: %v", err)
	}

	if !cid.Equals(target1) {
		t.Errorf("Expected CID %s, got %s", target1, cid)
	}

	if len(proof) == 0 {
		t.Error("Proof should not be empty")
	}

	// Verify proof
	valid, err := scheme.Verify(comm, "path1", target1, proof)
	if err != nil {
		t.Fatalf("Failed to verify: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}

	t.Logf("Proof verification successful")
}

func TestKZGCommitment_Update(t *testing.T) {
	scheme, err := kzg.New(nil)
	if err != nil {
		t.Fatalf("Failed to create KZG scheme: %v", err)
	}

	target1, _ := types.NewCID([]byte("target1_original"))
	target2, _ := types.NewCID([]byte("target2_data"))

	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("path1", target1),
		types.NewArcPair("path2", target2),
	)

	comm, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Update path1
	newTarget, _ := types.NewCID([]byte("target1_updated"))
	newComm, err := scheme.Update(comm, "path1", target1, newTarget)
	if err != nil {
		t.Fatalf("Failed to update: %v", err)
	}

	if string(newComm) == string(comm) {
		t.Error("New commitment should be different from old")
	}

	// Verify we can resolve the updated path
	updatedArcs := arcs.Clone()
	updatedArcs.Add("path1", newTarget)

	cid, proof, err := scheme.Prove(newComm, updatedArcs, "path1")
	if err != nil {
		t.Fatalf("Failed to prove updated commitment: %v", err)
	}

	if !cid.Equals(newTarget) {
		t.Errorf("Expected updated CID, got %s", cid)
	}

	// Verify proof
	valid, err := scheme.Verify(newComm, "path1", newTarget, proof)
	if err != nil {
		t.Fatalf("Failed to verify updated proof: %v", err)
	}

	if !valid {
		t.Error("Updated proof should be valid")
	}
}

func TestKZGCommitment_BatchUpdate(t *testing.T) {
	scheme, err := kzg.New(nil)
	if err != nil {
		t.Fatalf("Failed to create KZG scheme: %v", err)
	}

	target1, _ := types.NewCID([]byte("target1_original"))
	target2, _ := types.NewCID([]byte("target2_original"))

	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("path1", target1),
		types.NewArcPair("path2", target2),
	)

	comm, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Batch update
	newTarget1, _ := types.NewCID([]byte("target1_updated"))
	newTarget2, _ := types.NewCID([]byte("target2_updated"))

	updates := map[types.Path]struct {
		Old types.CID
		New types.CID
	}{
		"path1": {Old: target1, New: newTarget1},
		"path2": {Old: target2, New: newTarget2},
	}

	newComm, err := scheme.BatchUpdate(comm, updates)
	if err != nil {
		t.Fatalf("Failed to batch update: %v", err)
	}

	if string(newComm) == string(comm) {
		t.Error("New commitment should be different from old")
	}

	t.Logf("Batch update successful")
}

func TestKZGCommitment_InvalidUpdate(t *testing.T) {
	scheme, err := kzg.New(nil)
	if err != nil {
		t.Fatalf("Failed to create KZG scheme: %v", err)
	}

	target1, _ := types.NewCID([]byte("target1_original"))
	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("path1", target1),
	)

	comm, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Try to update with wrong old CID
	wrongOldCID, _ := types.NewCID([]byte("wrong_old"))
	newTarget, _ := types.NewCID([]byte("new_target"))

	_, err = scheme.Update(comm, "path1", wrongOldCID, newTarget)
	if err == nil {
		t.Error("Update with wrong old CID should fail")
	}
}

func TestKZGCommitment_PathNotFound(t *testing.T) {
	scheme, err := kzg.New(nil)
	if err != nil {
		t.Fatalf("Failed to create KZG scheme: %v", err)
	}

	target1, _ := types.NewCID([]byte("target1_data"))
	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("path1", target1),
	)

	comm, err := scheme.Commit(arcs)
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Try to prove non-existent path
	_, _, err = scheme.Prove(comm, arcs, "nonexistent")
	if err == nil {
		t.Error("Prove for non-existent path should fail")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}