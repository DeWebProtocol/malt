package mock

import (
	"testing"

	"github.com/dewebprotocol/malt/pkg/types"
)

func TestMockCommitmentCommit(t *testing.T) {
	mc := NewMockCommitment()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	comm, err := mc.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if comm.IsEmpty() {
		t.Error("Commitment should not be empty")
	}

	// Same arc set should produce same commitment
	comm2, err := mc.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if !comm.Equals(comm2) {
		t.Error("Same arc set should produce same commitment")
	}
}

func TestMockCommitmentProveVerify(t *testing.T) {
	mc := NewMockCommitment()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	comm, err := mc.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Prove
	target, proof, err := mc.Prove(comm, arcs, "link")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	if !target.Equals(cid) {
		t.Error("Prove returned wrong target")
	}

	if proof.IsEmpty() {
		t.Error("Proof should not be empty")
	}

	// Verify
	valid, err := mc.Verify(comm, "link", cid, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}

	// Verify with wrong target should fail
	wrongCID, _ := types.NewCID([]byte("wrong"))
	valid, _ = mc.Verify(comm, "link", wrongCID, proof)
	if valid {
		t.Error("Proof should be invalid for wrong target")
	}
}

func TestMockCommitmentUpdate(t *testing.T) {
	mc := NewMockCommitment()

	cid1, _ := types.NewCID([]byte("target1"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid1))

	comm, err := mc.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Update
	cid2, _ := types.NewCID([]byte("target2"))
	newComm, err := mc.Update(comm, "link", cid1, cid2)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	if newComm.Equals(comm) {
		t.Error("Update should produce different commitment")
	}

	// Verify old commitment still works
	target, _, err := mc.Prove(comm, arcs, "link")
	if err != nil {
		t.Fatalf("Prove on old commitment failed: %v", err)
	}
	if !target.Equals(cid1) {
		t.Error("Old commitment should still reference old target")
	}

	// Verify new commitment works with new target
	arcs.Add("link", cid2) // Update arc set
	target, proof, err := mc.Prove(newComm, arcs, "link")
	if err != nil {
		t.Fatalf("Prove on new commitment failed: %v", err)
	}
	if !target.Equals(cid2) {
		t.Error("New commitment should reference new target")
	}

	valid, _ := mc.Verify(newComm, "link", cid2, proof)
	if !valid {
		t.Error("Proof for new commitment should be valid")
	}
}

func TestMockCommitmentBatchUpdate(t *testing.T) {
	mc := NewMockCommitment()

	cid1, _ := types.NewCID([]byte("target1"))
	cid2, _ := types.NewCID([]byte("target2"))
	cid3, _ := types.NewCID([]byte("target3"))

	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("link1", cid1),
		types.NewArcPair("link2", cid2),
	)

	comm, err := mc.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Batch update
	updates := map[types.Path]struct {
		Old types.CID
		New types.CID
	}{
		"link1": {Old: cid1, New: cid3},
		"link2": {Old: cid2, New: cid3},
	}

	newComm, err := mc.BatchUpdate(comm, updates)
	if err != nil {
		t.Fatalf("BatchUpdate failed: %v", err)
	}

	if newComm.Equals(comm) {
		t.Error("BatchUpdate should produce different commitment")
	}
}

func TestMockCommitmentProveNonExistent(t *testing.T) {
	mc := NewMockCommitment()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	comm, err := mc.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Prove non-existent path
	_, _, err = mc.Prove(comm, arcs, "nonexistent")
	if err == nil {
		t.Error("Prove should fail for non-existent path")
	}
}

func TestMockCommitmentSize(t *testing.T) {
	mc := NewMockCommitment()

	if mc.Size() != 0 {
		t.Errorf("Initial size = %d, want 0", mc.Size())
	}

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	mc.Commit(arcs)

	if mc.Size() != 1 {
		t.Errorf("Size after commit = %d, want 1", mc.Size())
	}

	mc.Clear()

	if mc.Size() != 0 {
		t.Errorf("Size after clear = %d, want 0", mc.Size())
	}
}