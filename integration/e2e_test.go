// Package integration provides end-to-end integration tests for MALT.
// These tests exercise the full stack: Writer → EAT → SCE → Resolver.
package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// fakeCID creates a deterministic CID from a string seed.
func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

// newNode creates a fresh MALT node for testing.
func newNode(t *testing.T) *api.Node {
	t.Helper()
	node, err := api.NewNode()
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	t.Cleanup(func() { node.Close() })
	return node
}

// buildArcs creates a map of arc path → target CID.
func buildArcs(n int) map[string]cid.Cid {
	arcs := make(map[string]cid.Cid, n)
	for i := 0; i < n; i++ {
		arcs[fmt.Sprintf("arc%d", i)] = fakeCID(fmt.Sprintf("data%d", i))
	}
	return arcs
}

// ===== E2E: Create → Resolve =====

func TestE2E_CreateAndResolve(t *testing.T) {
	node := newNode(t)
	ctx := context.Background()

	// Create arcs
	arcs := buildArcs(10)
	snapshot := arcset.NewMapFrom(arcs)

	// Create structure
	w := node.SCE()
	root, err := w.Commit(snapshot)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	if !root.Defined() {
		t.Fatal("root CID is undefined after commit")
	}

	// Store arcs in EAT
	eat := node.EAT()
	if err := eat.Update(ctx, "default", root, cid.Undef, arcs); err != nil {
		t.Fatalf("EAT.Update failed: %v", err)
	}

	// Resolve each arc via EAT + SCE
	for path, expected := range arcs {
		// EAT lookup
		snap, err := eat.Snapshot(ctx, "default", root)
		if err != nil {
			t.Fatalf("Snapshot failed for %s: %v", path, err)
		}

		// SCE prove
		_, proof, err := w.Prove(root, snap, path)
		if err != nil {
			t.Fatalf("Prove failed for %s: %v", path, err)
		}

		// SCE verify
		valid, err := w.Verify(root, path, expected, proof)
		if err != nil {
			t.Fatalf("Verify failed for %s: %v", path, err)
		}
		if !valid {
			t.Errorf("proof invalid for %s", path)
		}
	}
}

// ===== E2E: Update → Resolve Cycle =====

func TestE2E_UpdateResolveCycle(t *testing.T) {
	node := newNode(t)
	ctx := context.Background()

	// Create initial structure
	arcs := buildArcs(10)
	snapshot := arcset.NewMapFrom(arcs)
	w := node.SCE()
	eat := node.EAT()

	root1, err := w.Commit(snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}
	eat.Update(ctx, "default", root1, cid.Undef, arcs)

	// Verify initial state
	path := "arc5"
	snap, _ := eat.Snapshot(ctx, "default", root1)
	_, proof, _ := w.Prove(root1, snap, path)
	valid, _ := w.Verify(root1, path, arcs[path], proof)
	if !valid {
		t.Fatal("initial proof invalid")
	}

	// Update arc5 to a new target
	newTarget := fakeCID("updated_data5")
	arcs[path] = newTarget
	snapshot2 := arcset.NewMapFrom(arcs)
	root2, err := w.Commit(snapshot2)
	if err != nil {
		t.Fatalf("Update commit failed: %v", err)
	}
	eat.Update(ctx, "default", root2, root1, arcs)

	// Resolve arc5 on new root
	snap2, err := eat.Snapshot(ctx, "default", root2)
	if err != nil {
		t.Fatalf("Snapshot after update failed: %v", err)
	}
	_, proof2, err := w.Prove(root2, snap2, path)
	if err != nil {
		t.Fatalf("Prove after update failed: %v", err)
	}
	valid, err = w.Verify(root2, path, newTarget, proof2)
	if err != nil || !valid {
		t.Fatalf("proof invalid after update: %v", err)
	}

	// Old root should still resolve to old target (use original snapshot)
	valid, _ = w.Verify(root1, path, fakeCID("data5"), proof)
	if !valid {
		t.Error("old root should still resolve to old target")
	}
}

// ===== E2E: Chained Updates with Lineage =====

func TestE2E_ChainedUpdatesWithLineage(t *testing.T) {
	node := newNode(t)
	ctx := context.Background()

	// Get lineage manager
	lm := node.LineageManager()

	eat := node.EAT()
	sce := node.SCE()

	// Start with initial arcs
	arcs := buildArcs(4)
	snapshot := arcset.NewMapFrom(arcs)

	root0, err := sce.Commit(snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}
	eat.Update(ctx, "default", root0, cid.Undef, arcs)
	lm.Record(ctx, root0, cid.Undef, len(arcs))

	roots := []cid.Cid{root0}
	currentArcs := make(map[string]cid.Cid)
	for k, v := range arcs {
		currentArcs[k] = v
	}

	// Perform 5 updates, each modifying 4 arcs
	for i := 0; i < 5; i++ {
		for j := 0; j < 4; j++ {
			path := fmt.Sprintf("arc%d", j)
			currentArcs[path] = fakeCID(fmt.Sprintf("v%d_%s", i+1, path))
		}
		snapshot := arcset.NewMapFrom(currentArcs)

		newRoot, err := sce.Commit(snapshot)
		if err != nil {
			t.Fatalf("Commit v%d failed: %v", i+1, err)
		}
		eat.Update(ctx, "default", newRoot, roots[len(roots)-1], currentArcs)
		lm.Record(ctx, newRoot, roots[len(roots)-1], len(currentArcs))

		roots = append(roots, newRoot)
	}

	// Verify lineage chain
	current := roots[len(roots)-1]
	ancestors, err := lm.Ancestors(ctx, current, 0)
	if err != nil {
		t.Fatalf("Ancestors failed: %v", err)
	}
	if len(ancestors) != 5 {
		t.Errorf("expected 5 ancestors, got %d", len(ancestors))
	}

	// Verify depth
	depth, err := lm.Depth(ctx, current)
	if err != nil {
		t.Fatalf("Depth failed: %v", err)
	}
	if depth != 6 { // 1 (root) + 5 updates
		t.Errorf("expected depth 6, got %d", depth)
	}

	// Verify latest version is resolvable
	snap, err := eat.Snapshot(ctx, "default", current)
	if err != nil {
		t.Fatalf("Snapshot for latest version failed: %v", err)
	}
	_, _, err = sce.Prove(current, snap, "arc0")
	if err != nil {
		t.Fatalf("Prove for latest version failed: %v", err)
	}
}

// ===== E2E: Insert and Delete Operations =====

func TestE2E_InsertDelete(t *testing.T) {
	node := newNode(t)
	ctx := context.Background()

	// Create initial structure
	arcs := buildArcs(10)
	snapshot := arcset.NewMapFrom(arcs)
	sce := node.SCE()
	eat := node.EAT()

	root, err := sce.Commit(snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}
	eat.Update(ctx, "default", root, cid.Undef, arcs)

	// Insert new arc
	newPath := "new_arc"
	newTarget := fakeCID("new_data")
	arcs[newPath] = newTarget
	snapshot2 := arcset.NewMapFrom(arcs)
	root2, err := sce.Commit(snapshot2)
	if err != nil {
		t.Fatalf("Insert commit failed: %v", err)
	}
	eat.Update(ctx, "default", root2, root, arcs)

	// Verify new arc exists
	target, err := eat.Get(ctx, "default", root2, newPath)
	if err != nil {
		t.Fatalf("Get new arc failed: %v", err)
	}
	if !target.Equals(newTarget) {
		t.Errorf("new arc target mismatch: got %s, want %s", target, newTarget)
	}

	// Verify initial arc still exists
	arc0Target, err := eat.Get(ctx, "default", root2, "arc0")
	if err != nil {
		t.Fatalf("Get arc0 failed: %v", err)
	}
	if !arc0Target.Equals(fakeCID("data0")) {
		t.Errorf("arc0 target mismatch: got %s", arc0Target)
	}

	// Delete arc0 - EAT requires explicit cid.Undef to delete
	delPath := "arc0"
	arcs[delPath] = cid.Undef
	snapshot3 := arcset.NewMapFrom(arcs)
	root3, err := sce.Commit(snapshot3)
	if err != nil {
		t.Fatalf("Delete commit failed: %v", err)
	}
	eat.Update(ctx, "default", root3, root2, arcs)

	// Verify deleted arc is gone from new root
	_, err = eat.Get(ctx, "default", root3, delPath)
	if err == nil {
		t.Error("deleted arc should not be found on new root")
	}

	// Verify remaining arcs still work
	remainingTarget, err := eat.Get(ctx, "default", root3, "arc1")
	if err != nil {
		t.Errorf("arc1 should exist on root3: %v", err)
	}
	if !remainingTarget.Equals(fakeCID("data1")) {
		t.Errorf("arc1 on root3 has wrong value: got %s", remainingTarget)
	}

	// Count arcs on new root (exclude deleted arc from count)
	snap3, err := eat.Snapshot(ctx, "default", root3)
	if err != nil {
		t.Fatalf("Snapshot for root3 failed: %v", err)
	}
	count := 0
	iter := snap3.Iterate()
	for {
		_, _, ok := iter.Next()
		if !ok {
			break
		}
		count++
	}
	// Should have 10 (original) + 1 (inserted) - 1 (deleted) = 10 arcs
	if count != 10 {
		t.Errorf("expected 10 arcs after insert+delete, got %d", count)
	}
}
