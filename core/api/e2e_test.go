// Package api provides end-to-end integration tests for the MALT Go API.
// These tests exercise the full stack: Graph Commit → SCE → EAT → Resolver → Verify.
package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func newTestGraph(t *testing.T) (*Node, *graph.Graph) {
	t.Helper()
	node, err := NewNode()
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	t.Cleanup(func() { node.Close() })
	g, err := node.NewGraph("test-graph")
	if err != nil {
		t.Fatalf("NewGraph failed: %v", err)
	}
	return node, g
}

func buildArcs(n int) map[string]cid.Cid {
	arcs := make(map[string]cid.Cid, n)
	for i := 0; i < n; i++ {
		arcs[fmt.Sprintf("arc%d", i)] = fakeCID(fmt.Sprintf("data%d", i))
	}
	return arcs
}

// ===== Create → Resolve =====

func TestAPI_CreateAndResolve(t *testing.T) {
	_, g := newTestGraph(t)
	ctx := context.Background()

	arcs := buildArcs(10)
	snapshot := arcset.NewMapFrom(arcs)

	root, err := g.Commit(ctx, snapshot)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if !root.Defined() {
		t.Fatal("root CID is undefined after commit")
	}

	for path, expected := range arcs {
		result, err := g.Resolver().Resolve(root, path)
		if err != nil {
			t.Fatalf("Resolve failed for %s: %v", path, err)
		}
		if !result.Target.Equals(expected) {
			t.Errorf("target mismatch for %s: got %s, want %s", path, result.Target, expected)
		}

		proof := graph.NewTranscriptProof(result.Transcript)
		valid, err := g.Verify(ctx, root, proof, expected)
		if err != nil {
			t.Fatalf("Verify failed for %s: %v", path, err)
		}
		if !valid {
			t.Errorf("proof invalid for %s", path)
		}
	}
}

// ===== Update → Resolve Cycle =====

func TestAPI_UpdateResolveCycle(t *testing.T) {
	_, g := newTestGraph(t)
	ctx := context.Background()

	arcs := buildArcs(10)
	snapshot := arcset.NewMapFrom(arcs)

	root1, err := g.Commit(ctx, snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}

	// Verify initial state
	path := "arc5"
	result, err := g.Resolver().Resolve(root1, path)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	proof := graph.NewTranscriptProof(result.Transcript)
	valid, _ := g.Verify(ctx, root1, proof, arcs[path])
	if !valid {
		t.Fatal("initial proof invalid")
	}

	// Update arc5
	newTarget := fakeCID("updated_data5")
	arcs[path] = newTarget
	snapshot2 := arcset.NewMapFrom(arcs)
	root2, err := g.Commit(ctx, snapshot2)
	if err != nil {
		t.Fatalf("Update commit failed: %v", err)
	}

	// Resolve on new root
	result2, err := g.Resolver().Resolve(root2, path)
	if err != nil {
		t.Fatalf("Resolve after update failed: %v", err)
	}
	proof2 := graph.NewTranscriptProof(result2.Transcript)
	valid, err = g.Verify(ctx, root2, proof2, newTarget)
	if err != nil || !valid {
		t.Fatalf("proof invalid after update: %v", err)
	}

	// Old root should still resolve to old target
	result1, err := g.Resolver().Resolve(root1, path)
	if err != nil {
		t.Fatalf("Resolve on old root failed: %v", err)
	}
	proof1 := graph.NewTranscriptProof(result1.Transcript)
	valid, _ = g.Verify(ctx, root1, proof1, fakeCID("data5"))
	if !valid {
		t.Error("old root should still resolve to old target")
	}
}

// ===== Chained Updates with Lineage =====

func TestAPI_ChainedUpdatesWithLineage(t *testing.T) {
	node, g := newTestGraph(t)
	ctx := context.Background()

	lm := node.LineageManager()

	arcs := buildArcs(4)
	snapshot := arcset.NewMapFrom(arcs)

	root0, err := g.Commit(ctx, snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}
	lm.Record(ctx, root0, cid.Undef, len(arcs))

	roots := []cid.Cid{root0}
	currentArcs := make(map[string]cid.Cid)
	for k, v := range arcs {
		currentArcs[k] = v
	}

	// 5 updates, each modifying 4 arcs
	for i := 0; i < 5; i++ {
		for j := 0; j < 4; j++ {
			path := fmt.Sprintf("arc%d", j)
			currentArcs[path] = fakeCID(fmt.Sprintf("v%d_%s", i+1, path))
		}
		snapshot := arcset.NewMapFrom(currentArcs)

		newRoot, err := g.Commit(ctx, snapshot)
		if err != nil {
			t.Fatalf("Commit v%d failed: %v", i+1, err)
		}
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

	depth, err := lm.Depth(ctx, current)
	if err != nil {
		t.Fatalf("Depth failed: %v", err)
	}
	if depth != 6 {
		t.Errorf("expected depth 6, got %d", depth)
	}

	// Verify latest version is resolvable
	_, err = g.Resolver().Resolve(current, "arc0")
	if err != nil {
		t.Fatalf("Resolve for latest version failed: %v", err)
	}
}

// ===== Insert and Delete =====

func TestAPI_InsertDelete(t *testing.T) {
	_, g := newTestGraph(t)
	ctx := context.Background()

	arcs := buildArcs(10)
	snapshot := arcset.NewMapFrom(arcs)

	root, err := g.Commit(ctx, snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}
	_ = root

	// Insert new arc
	newPath := "new_arc"
	newTarget := fakeCID("new_data")
	arcs[newPath] = newTarget
	snapshot2 := arcset.NewMapFrom(arcs)
	root2, err := g.Commit(ctx, snapshot2)
	if err != nil {
		t.Fatalf("Insert commit failed: %v", err)
	}

	// Verify new arc
	result, err := g.Resolver().Resolve(root2, newPath)
	if err != nil {
		t.Fatalf("Resolve new arc failed: %v", err)
	}
	if !result.Target.Equals(newTarget) {
		t.Errorf("new arc target mismatch: got %s, want %s", result.Target, newTarget)
	}

	// Verify initial arc still exists
	arc0Result, err := g.Resolver().Resolve(root2, "arc0")
	if err != nil {
		t.Fatalf("Resolve arc0 failed: %v", err)
	}
	if !arc0Result.Target.Equals(fakeCID("data0")) {
		t.Errorf("arc0 target mismatch: got %s", arc0Result.Target)
	}

	// Delete arc0
	arcs["arc0"] = cid.Undef
	snapshot3 := arcset.NewMapFrom(arcs)
	root3, err := g.Commit(ctx, snapshot3)
	if err != nil {
		t.Fatalf("Delete commit failed: %v", err)
	}

	// Verify deleted
	_, err = g.Resolver().Resolve(root3, "arc0")
	if err == nil {
		t.Error("deleted arc should not be found on new root")
	}

	// Verify remaining arcs
	remainingResult, err := g.Resolver().Resolve(root3, "arc1")
	if err != nil {
		t.Errorf("arc1 should exist on root3: %v", err)
	}
	if !remainingResult.Target.Equals(fakeCID("data1")) {
		t.Errorf("arc1 on root3 has wrong value: got %s", remainingResult.Target)
	}

	// Count arcs
	snap3, err := g.Snapshot(ctx, root3)
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
	if count != 10 {
		t.Errorf("expected 10 arcs after insert+delete, got %d", count)
	}
}
