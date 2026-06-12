package writer

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	semanticmapping "github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/runtime/arctable/overwrite"
	"github.com/dewebprotocol/malt/runtime/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// TestBatchUpdateArcs_Atomicity verifies that BatchUpdateArcs is truly atomic:
// if any update in the batch fails, no updates are applied.
func TestBatchUpdateArcs_Atomicity(t *testing.T) {
	ctx := context.Background()
	namespace := "test"

	// Setup
	kv := memory.New()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	arctable, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}
	maps, err := radix.NewMap(scheme, arctable)
	if err != nil {
		t.Fatalf("NewMap failed: %v", err)
	}
	writer := NewWriter(maps, arctable)

	// Test Case 1: Successful batch update
	valueA := makeCID(t, "value-a")
	payload := makeCID(t, "payload")

	initialArcs, err := arcset.NewArcSet(map[string]cid.Cid{
		"@payload": payload,
		"a":        valueA,
	})
	if err != nil {
		t.Fatalf("NewArcSet failed: %v", err)
	}

	root, err := writer.CreateStructure(ctx, namespace, initialArcs)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Successful batch update
	newValueA := makeCID(t, "new-value-a")
	updates := map[string]cid.Cid{
		"a": newValueA,
	}

	result, err := writer.BatchUpdateArcs(ctx, namespace, root, updates)
	if err != nil {
		t.Fatalf("Valid BatchUpdateArcs failed: %v", err)
	}
	t.Logf("Batch update succeeded, new root: %v", result.NewRoot)

	// Test Case 2: Failing batch update - try to delete @payload (mandatory)
	// This should fail and NOT modify any state
	initialArcs2, err := arcset.NewArcSet(map[string]cid.Cid{
		"@payload": payload,
		"x":        valueA,
	})
	if err != nil {
		t.Fatalf("NewArcSet failed: %v", err)
	}
	root2, err := writer.CreateStructure(ctx, namespace+"_fail", initialArcs2)
	if err != nil {
		t.Fatalf("CreateStructure failed: %v", err)
	}

	// Try batch update that includes deleting @payload (which should fail)
	failingUpdates := map[string]cid.Cid{
		"x":        makeCID(t, "new-x"), // Valid update
		"@payload": cid.Undef,           // INVALID: cannot delete @payload
	}

	_, err = writer.BatchUpdateArcs(ctx, namespace+"_fail", root2, failingUpdates)
	if err == nil {
		t.Fatalf("BatchUpdateArcs should have failed due to @payload deletion, but succeeded")
	}
	t.Logf("BatchUpdateArcs correctly failed: %v", err)

	// Verify atomicity: 'x' should still have original value
	// The update to 'x' should NOT have been applied because the batch failed
	gotX, err := writer.GetArc(ctx, namespace+"_fail", root2, "x")
	if err != nil {
		t.Fatalf("GetArc(x) after failed batch: %v", err)
	}
	if !gotX.Equals(valueA) {
		t.Errorf("After failed batch: x = %v, want original %v (atomicity violated!)", gotX, valueA)
	}

	// Verify @payload still exists with original value
	gotPayload, err := writer.GetArc(ctx, namespace+"_fail", root2, "@payload")
	if err != nil {
		t.Fatalf("GetArc(@payload) after failed batch: %v", err)
	}
	if !gotPayload.Equals(payload) {
		t.Errorf("After failed batch: @payload = %v, want original %v", gotPayload, payload)
	}

	t.Log("Atomicity verified: failed batch did not modify any arcs")
	t.Log("All atomicity tests passed")
}

// TestSemanticBatchUpdate_MidBatchFailure tests the deferred persistence path in
// semantic.BatchUpdate: if the second update in a batch fails, the first update's
// changes must NOT be persisted (P2 regression guard).
func TestSemanticBatchUpdate_MidBatchFailure(t *testing.T) {
	ctx := context.Background()
	namespace := "test-mid-batch"

	kv := memory.New()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme: %v", err)
	}
	at, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable: %v", err)
	}
	maps, err := radix.NewMap(scheme, at)
	if err != nil {
		t.Fatalf("NewMap: %v", err)
	}

	// Commit initial state: key-a and key-b
	valueA := makeCID(t, "value-a")
	valueB := makeCID(t, "value-b")
	root, err := maps.Commit(ctx, namespace, semanticmapping.NewViewFrom(map[string]cid.Cid{
		"key-a": valueA,
		"key-b": valueB,
	}))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Determine the intermediate root CID (state after inserting key-new alongside key-a/key-b).
	// Commit to a probe namespace: CID is content-addressed so it equals what loop-Update
	// would produce in the main namespace, but data goes to the probe namespace only.
	valueNew := makeCID(t, "value-new")
	intermediateRoot, err := maps.Commit(ctx, namespace+"_probe", semanticmapping.NewViewFrom(map[string]cid.Cid{
		"key-a":   valueA,
		"key-b":   valueB,
		"key-new": valueNew,
	}))
	if err != nil {
		t.Fatalf("probe Commit: %v", err)
	}

	// Batch: first op inserts key-new (valid), second op uses wrong OldValue (fails)
	wrongOld := makeCID(t, "wrong-old")
	_, err = maps.BatchUpdate(ctx, namespace, root, []semanticmapping.BatchUpdate{
		{Key: arcset.CanonicalizePath("key-new"), OldValue: cid.Undef, NewValue: valueNew},
		{Key: arcset.CanonicalizePath("key-a"), OldValue: wrongOld, NewValue: makeCID(t, "value-x")},
	})
	if err == nil {
		t.Fatal("BatchUpdate should have failed due to OldValue mismatch, but succeeded")
	}
	t.Logf("BatchUpdate correctly failed: %v", err)

	// Regression guard: if BatchUpdate regresses to loop-calling Update, the intermediate
	// root's node data gets written to the main namespace before the second update fails.
	// Verify the intermediate root is NOT readable in the main namespace.
	_, _, err = maps.Prove(ctx, namespace, intermediateRoot, arcset.CanonicalizePath("key-new"))
	if err == nil {
		t.Error("atomicity violated: intermediate root was persisted in main namespace during failed batch")
	}

	// key-new must NOT be findable under the original root either.
	_, _, err = maps.Prove(ctx, namespace, root, arcset.CanonicalizePath("key-new"))
	if err == nil {
		t.Error("atomicity violated: key-new was persisted despite mid-batch failure")
	}

	// key-a must still have original value
	bindingA, _, err := maps.Prove(ctx, namespace, root, arcset.CanonicalizePath("key-a"))
	if err != nil {
		t.Fatalf("Prove(key-a): %v", err)
	}
	if !bindingA.Value.Equals(valueA) {
		t.Errorf("atomicity violated: key-a = %v, want original %v", bindingA.Value, valueA)
	}
}

func makeCID(t *testing.T, data string) cid.Cid {
	t.Helper()
	mhash, err := mh.Sum([]byte(data), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("Build CID failed: %v", err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}
