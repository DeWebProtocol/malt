// Package node provides end-to-end integration tests for the MALT Go API.
// These tests exercise the full stack: Graph Commit -> semantic layer ->
// ArcTable -> Resolver -> Verify.
package node

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/graph"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func newTestGraph(t *testing.T) (*Node, graph.Runtime) {
	t.Helper()
	node, err := NewNode(WithConfig(testRuntimeConfig(t)))
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
	arcs := make(map[string]cid.Cid, n+1)
	arcs["@payload"] = fakeCID("payload")
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
	snapshot := arcset.NewSetFrom(arcs)

	root, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot)
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

		valid, err := g.Resolver().VerifyTranscript(root, result.Transcript)
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
	snapshot := arcset.NewSetFrom(arcs)

	root1, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}

	// Verify initial state
	path := "arc5"
	result, err := g.Resolver().Resolve(root1, path)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !result.Target.Equals(arcs[path]) {
		t.Fatalf("initial target mismatch: got %s, want %s", result.Target, arcs[path])
	}
	valid, err := g.Resolver().VerifyTranscript(root1, result.Transcript)
	if err != nil {
		t.Fatalf("initial transcript verification failed: %v", err)
	}
	if !valid {
		t.Fatal("initial proof invalid")
	}

	// Update arc5
	newTarget := fakeCID("updated_data5")
	arcs[path] = newTarget
	snapshot2 := arcset.NewSetFrom(arcs)
	root2, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot2)
	if err != nil {
		t.Fatalf("Update commit failed: %v", err)
	}

	// Resolve on new root
	result2, err := g.Resolver().Resolve(root2, path)
	if err != nil {
		t.Fatalf("Resolve after update failed: %v", err)
	}
	if !result2.Target.Equals(newTarget) {
		t.Fatalf("updated target mismatch: got %s, want %s", result2.Target, newTarget)
	}
	valid, err = g.Resolver().VerifyTranscript(root2, result2.Transcript)
	if err != nil || !valid {
		t.Fatalf("proof invalid after update: %v", err)
	}

	// Old root should still resolve to old target
	result1, err := g.Resolver().Resolve(root1, path)
	if err != nil {
		t.Fatalf("Resolve on old root failed: %v", err)
	}
	oldTarget := fakeCID("data5")
	if !result1.Target.Equals(oldTarget) {
		t.Fatalf("old root target mismatch: got %s, want %s", result1.Target, oldTarget)
	}
	valid, err = g.Resolver().VerifyTranscript(root1, result1.Transcript)
	if err != nil {
		t.Fatalf("old root transcript verification failed: %v", err)
	}
	if !valid {
		t.Error("old root should still resolve to old target")
	}
}

// ===== Chained Root Updates =====

func TestAPI_ChainedUpdatesResolveLatestRoot(t *testing.T) {
	_, g := newTestGraph(t)
	ctx := context.Background()

	arcs := buildArcs(4)
	snapshot := arcset.NewSetFrom(arcs)

	root0, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot)
	if err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}

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
		snapshot := arcset.NewSetFrom(currentArcs)

		newRoot, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot)
		if err != nil {
			t.Fatalf("Commit v%d failed: %v", i+1, err)
		}

		roots = append(roots, newRoot)
	}

	current := roots[len(roots)-1]
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
	snapshot := arcset.NewSetFrom(arcs)

	if _, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot); err != nil {
		t.Fatalf("Initial commit failed: %v", err)
	}

	// Insert new arc
	newPath := "new_arc"
	newTarget := fakeCID("new_data")
	arcs[newPath] = newTarget
	snapshot2 := arcset.NewSetFrom(arcs)
	root2, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot2)
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

	// Delete arc0 through the update path. Full Commit creates a fresh
	// structure from the provided snapshot, while delete semantics are carried
	// by the unified update procedure.
	updateResult, err := g.Writer().BatchUpdateArcs(ctx, g.Namespace(), root2, map[string]cid.Cid{"arc0": cid.Undef})
	if err != nil {
		t.Fatalf("Delete commit failed: %v", err)
	}
	root3 := updateResult.NewRoot

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
	snap3, err := g.Writer().GetSnapshot(ctx, g.Namespace(), root3)
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
	if count != 11 {
		t.Errorf("expected 11 arcs including @payload after insert+delete, got %d", count)
	}
}

func testRuntimeConfig(t *testing.T) *config.Config {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "badger"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "mock"
	return cfg
}
