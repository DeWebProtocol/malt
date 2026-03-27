package malt

import (
	"testing"

	"github.com/dewebprotocol/malt/pkg/types"
)

func TestMALTCreateStructure(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	if comm.IsEmpty() {
		t.Error("Commitment should not be empty")
	}
}

func TestMALTResolve(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Resolve
	resolved, proof, err := m.Resolve(comm, "link")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if !resolved.Equals(cid) {
		t.Error("Resolved CID should match original")
	}

	if proof.IsEmpty() {
		t.Error("Proof should not be empty")
	}
}

func TestMALTVerify(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	resolved, proof, err := m.Resolve(comm, "link")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Verify
	valid, err := m.Verify(comm, "link", resolved, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Proof should be valid")
	}
}

func TestMALTUpdateArc(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid1, _ := types.NewCID([]byte("target1"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid1))

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Update arc
	cid2, _ := types.NewCID([]byte("target2"))
	newComm, err := m.UpdateArc(comm, "link", cid2)
	if err != nil {
		t.Fatalf("UpdateArc failed: %v", err)
	}

	if newComm.Equals(comm) {
		t.Error("Update should produce new commitment")
	}

	// Resolve with new commitment
	resolved, proof, err := m.Resolve(newComm, "link")
	if err != nil {
		t.Fatalf("Resolve after update failed: %v", err)
	}

	if !resolved.Equals(cid2) {
		t.Error("Resolved should return new target")
	}

	// Verify
	valid, _ := m.Verify(newComm, "link", cid2, proof)
	if !valid {
		t.Error("Proof should be valid for new commitment")
	}
}

func TestMALTAddArc(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid1, _ := types.NewCID([]byte("target1"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link1", cid1))

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Add new arc
	cid2, _ := types.NewCID([]byte("target2"))
	newComm, err := m.AddArc(comm, "link2", cid2)
	if err != nil {
		t.Fatalf("AddArc failed: %v", err)
	}

	// Resolve new arc
	resolved, _, err := m.Resolve(newComm, "link2")
	if err != nil {
		t.Fatalf("Resolve new arc failed: %v", err)
	}

	if !resolved.Equals(cid2) {
		t.Error("Resolved should return added target")
	}

	// Old arc should still exist
	resolved, _, err = m.Resolve(newComm, "link1")
	if err != nil {
		t.Fatalf("Resolve old arc failed: %v", err)
	}

	if !resolved.Equals(cid1) {
		t.Error("Old arc should still be accessible")
	}
}

func TestMALTRemoveArc(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(
		types.NewArcPair("link1", cid),
		types.NewArcPair("link2", cid),
	)

	comm, err := m.CreateStructure(arcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Remove arc
	newComm, err := m.RemoveArc(comm, "link2")
	if err != nil {
		t.Fatalf("RemoveArc failed: %v", err)
	}

	// Removed arc should not exist
	_, _, err = m.Resolve(newComm, "link2")
	if err == nil {
		t.Error("Removed arc should not be resolvable")
	}

	// Other arc should still exist
	resolved, _, err := m.Resolve(newComm, "link1")
	if err != nil {
		t.Fatalf("Resolve remaining arc failed: %v", err)
	}

	if !resolved.Equals(cid) {
		t.Error("Remaining arc should be accessible")
	}
}

func TestMALTGetLineage(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid1, _ := types.NewCID([]byte("target1"))
	cid2, _ := types.NewCID([]byte("target2"))

	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid1))
	comm, _ := m.CreateStructure(arcs)

	// Multiple updates create lineage
	comm2, _ := m.UpdateArc(comm, "link", cid2)

	// Get lineage
	lineage, err := m.GetLineage(comm2)
	if err != nil {
		t.Fatalf("GetLineage failed: %v", err)
	}

	if len(lineage) < 1 {
		t.Error("Lineage should have at least one entry")
	}

	// First entry should be the latest commitment
	if !lineage[0].Equals(comm2) {
		t.Error("First lineage entry should be latest commitment")
	}
}

func TestMALTStats(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	stats := m.Stats()
	if stats.StructureCount != 0 {
		t.Errorf("Initial structure count = %d, want 0", stats.StructureCount)
	}

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))
	m.CreateStructure(arcs)

	stats = m.Stats()
	if stats.StructureCount != 1 {
		t.Errorf("Structure count after create = %d, want 1", stats.StructureCount)
	}
}

func TestMALTDirectOperations(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer m.Close()

	cid, _ := types.NewCID([]byte("target"))
	arcs := types.NewArcSetFromPairs(types.NewArcPair("link", cid))

	// Commit directly (without storing)
	comm, err := m.CommitDirectly(arcs)
	if err != nil {
		t.Fatalf("CommitDirectly failed: %v", err)
	}

	// Prove directly
	target, proof, err := m.ProveDirectly(comm, arcs, "link")
	if err != nil {
		t.Fatalf("ProveDirectly failed: %v", err)
	}

	if !target.Equals(cid) {
		t.Error("Direct prove returned wrong target")
	}

	// Verify directly
	valid, err := m.VerifyDirectly(comm, "link", cid, proof)
	if err != nil {
		t.Fatalf("VerifyDirectly failed: %v", err)
	}

	if !valid {
		t.Error("Direct verify should be valid")
	}
}