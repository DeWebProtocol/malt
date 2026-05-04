package explicit_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/bloom"
	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const testNamespace = "test-bucket"

// makeCID creates a CID from an integer for testing purposes.
func makeCID(n int) cid.Cid {
	data := []byte{byte(n)}
	h, _ := mh.Sum(data, mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, h)
}

// newTestArcTable creates a fresh ArcTable backed by an in-memory KVStore.
func newTestArcTable() *overwrite.ArcTable {
	kv := kvmemory.New()
	e, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		panic(err)
	}
	return e
}

// newTestComponents creates a complete set of test components:
// ArcTable, mapping semantic, and KZG scheme.
func newTestComponents() (*overwrite.ArcTable, mapping.Semantics, *kzg.Scheme) {
	e := newTestArcTable()
	scheme, err := kzg.NewScheme()
	if err != nil {
		panic(err)
	}
	semantic, err := mappingradix.NewMap(scheme, e)
	if err != nil {
		panic(err)
	}
	return e, semantic, scheme
}

// setupArcSet commits arcs to semantic layer and stores them in ArcTable.
func setupArcSet(t *testing.T, e *overwrite.ArcTable, semantic mapping.Semantics, arcsMap map[string]cid.Cid) cid.Cid {
	t.Helper()
	root, err := semantic.Commit(context.Background(), testNamespace, mapping.NewViewFrom(arcsMap))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	ctx := context.Background()
	if err := e.Update(ctx, testNamespace, root, cid.Undef, arcset.NewSetFrom(arcsMap)); err != nil {
		t.Fatalf("ArcTable.Update failed: %v", err)
	}
	return root
}

func TestResolve_LongestPrefixMatch(t *testing.T) {
	e, semantic, _ := newTestComponents()

	// Create target CIDs
	target1 := makeCID(1)
	target2 := makeCID(2)
	target3 := makeCID(3)

	// Set up arcs: "a" -> target1, "a/b" -> target2, "a/b/c" -> target3
	arcsMap := map[string]cid.Cid{
		"a":     target1,
		"a/b":   target2,
		"a/b/c": target3,
	}
	root := setupArcSet(t, e, semantic, arcsMap)

	// Create resolver
	r := explicit.NewResolver(e, semantic, testNamespace)

	// Resolve "a/b/c/d" should match longest prefix "a/b/c" -> target3
	matchedPath, target, ev, err := r.Resolve(root, "a/b/c/d")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if matchedPath != "a/b/c" {
		t.Errorf("matchedPath = %q, want %q", matchedPath, "a/b/c")
	}
	if !target.Equals(target3) {
		t.Errorf("target = %v, want %v", target, target3)
	}
	if ev == nil {
		t.Fatal("evidence should not be nil")
	}
	if _, ok := ev.(*evidence.ExplicitEvidence); !ok {
		t.Errorf("evidence type = %T, want *evidence.ExplicitEvidence", ev)
	}

	// Verify the evidence
	valid, err := r.Verify(root, matchedPath, target, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Error("Verify should return true for valid evidence")
	}

	// Resolve "a/b/x" should match longest prefix "a/b" -> target2
	matchedPath, target, ev, err = r.Resolve(root, "a/b/x")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if matchedPath != "a/b" {
		t.Errorf("matchedPath = %q, want %q", matchedPath, "a/b")
	}
	if !target.Equals(target2) {
		t.Errorf("target = %v, want %v", target, target2)
	}

	// Verify this evidence too
	valid, err = r.Verify(root, matchedPath, target, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Error("Verify should return true for valid evidence")
	}

	// Verify that "a/b" is still stored in the snapshot (needed for semantic.Prove)
	// by resolving "a/x" -> should match "a" -> target1
	matchedPath, target, ev, err = r.Resolve(root, "a/x")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if matchedPath != "a" {
		t.Errorf("matchedPath = %q, want %q", matchedPath, "a")
	}
	if !target.Equals(target1) {
		t.Errorf("target = %v, want %v", target, target1)
	}

	// Verify
	valid, err = r.Verify(root, matchedPath, target, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Error("Verify should return true for valid evidence")
	}

}

func TestResolve_ExactMatch(t *testing.T) {
	e, semantic, _ := newTestComponents()

	target1 := makeCID(1)
	target2 := makeCID(2)

	arcsMap := map[string]cid.Cid{
		"a":   target1,
		"a/b": target2,
	}
	root := setupArcSet(t, e, semantic, arcsMap)

	r := explicit.NewResolver(e, semantic, testNamespace)

	// Exact match: resolve "a/b" should return "a/b" -> target2
	matchedPath, target, ev, err := r.Resolve(root, "a/b")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if matchedPath != "a/b" {
		t.Errorf("matchedPath = %q, want %q", matchedPath, "a/b")
	}
	if !target.Equals(target2) {
		t.Errorf("target = %v, want %v", target, target2)
	}
	if ev == nil {
		t.Fatal("evidence should not be nil")
	}

	// Verify the evidence
	valid, err := r.Verify(root, matchedPath, target, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Error("Verify should return true for valid evidence")
	}
}

func TestResolve_NoMatch(t *testing.T) {
	e, semantic, _ := newTestComponents()

	target := makeCID(1)
	arcsMap := map[string]cid.Cid{
		"a/b": target,
	}
	root := setupArcSet(t, e, semantic, arcsMap)

	r := explicit.NewResolver(e, semantic, testNamespace)

	// Resolve "x/y/z" has no matching prefix
	_, _, _, err := r.Resolve(root, "x/y/z")
	if err == nil {
		t.Fatal("expected error for non-matching path, got nil")
	}
}

func TestResolve_EmptyPath(t *testing.T) {
	e, semantic, _ := newTestComponents()

	target := makeCID(1)
	arcsMap := map[string]cid.Cid{
		"a": target,
	}
	root := setupArcSet(t, e, semantic, arcsMap)

	r := explicit.NewResolver(e, semantic, testNamespace)

	// Empty path should error with "path is empty"
	_, _, _, err := r.Resolve(root, "")
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
	if err.Error() != "path is empty" {
		t.Errorf("error = %q, want %q", err.Error(), "path is empty")
	}
}

func TestResolve_UndefinedRoot(t *testing.T) {
	e, semantic, _ := newTestComponents()

	r := explicit.NewResolver(e, semantic, testNamespace)

	// Undefined root should error with "root is not defined"
	_, _, _, err := r.Resolve(cid.Undef, "a/b")
	if err == nil {
		t.Fatal("expected error for undefined root, got nil")
	}
	if err.Error() != "root is not defined" {
		t.Errorf("error = %q, want %q", err.Error(), "root is not defined")
	}
}

func TestVerify_ValidProof(t *testing.T) {
	e, semantic, _ := newTestComponents()

	target := makeCID(1)
	arcsMap := map[string]cid.Cid{
		"a/b": target,
	}
	root := setupArcSet(t, e, semantic, arcsMap)

	r := explicit.NewResolver(e, semantic, testNamespace)

	// First resolve to get valid evidence
	matchedPath, resolvedTarget, ev, err := r.Resolve(root, "a/b")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Verify the evidence
	valid, err := r.Verify(root, matchedPath, resolvedTarget, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Error("Verify should return true for valid evidence")
	}
}

func TestVerify_WrongProof(t *testing.T) {
	e, semantic, _ := newTestComponents()

	target := makeCID(1)
	arcsMap := map[string]cid.Cid{
		"a/b": target,
	}
	root := setupArcSet(t, e, semantic, arcsMap)

	r := explicit.NewResolver(e, semantic, testNamespace)

	// Resolve to get a valid evidence
	matchedPath, resolvedTarget, ev, err := r.Resolve(root, "a/b")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Create evidence with tampered proof bytes
	originalProof := ev.Bytes()
	wrongProof := make([]byte, len(originalProof))
	copy(wrongProof, originalProof)
	// Flip a byte in the proof to corrupt it
	if len(wrongProof) > 0 {
		wrongProof[0] ^= 0xFF
	}
	wrongEv := evidence.NewExplicitEvidence(wrongProof)

	// Verify should fail with the wrong proof
	valid, err := r.Verify(root, matchedPath, resolvedTarget, wrongEv)
	if err != nil {
		// An error is acceptable for a corrupted proof
		t.Logf("Verify returned error (expected for wrong proof): %v", err)
		return
	}
	if valid {
		t.Error("Verify should return false for wrong proof")
	}
}

func TestVerify_NilEvidence(t *testing.T) {
	e, semantic, _ := newTestComponents()

	r := explicit.NewResolver(e, semantic, testNamespace)
	root := makeCID(1)
	target := makeCID(2)

	// Nil evidence should error
	_, err := r.Verify(root, "a/b", target, nil)
	if err == nil {
		t.Fatal("expected error for nil evidence, got nil")
	}
	if err.Error() != "evidence is nil" {
		t.Errorf("error = %q, want %q", err.Error(), "evidence is nil")
	}
}

func TestVerify_WrongEvidenceType(t *testing.T) {
	e, semantic, _ := newTestComponents()

	r := explicit.NewResolver(e, semantic, testNamespace)
	root := makeCID(1)
	target := makeCID(2)

	// Pass ImplicitEvidence instead of ExplicitEvidence
	implicitEv := evidence.NewImplicitEvidence([]byte("some block content"))
	_, err := r.Verify(root, "a/b", target, implicitEv)
	if err == nil {
		t.Fatal("expected error for wrong evidence type, got nil")
	}
	// The error message should indicate the wrong type
	if err.Error() == "" {
		t.Error("error message should not be empty")
	}
}

func TestBloomFilterWithResolver(t *testing.T) {
	// Test that the resolver works correctly when a bloom filter is configured.
	kv := kvmemory.New()
	bloomCache := bloom.NewBloomCache(kv, 100)
	e, err := overwrite.NewArcTable(
		overwrite.WithKVStore(kv),
		overwrite.WithBloomCache(bloomCache),
	)
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	semantic, err := mappingradix.NewMap(scheme, e)
	if err != nil {
		t.Fatalf("radix.NewMap failed: %v", err)
	}

	ctx := context.Background()
	namespace := "bloom-test"

	target := makeCID(42)
	arcsMap := map[string]cid.Cid{
		"data/file": target,
	}

	root, err := semantic.Commit(ctx, namespace, mapping.NewViewFrom(arcsMap))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := e.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(arcsMap)); err != nil {
		t.Fatalf("ArcTable.Update failed: %v", err)
	}

	r := explicit.NewResolver(e, semantic, namespace)

	matchedPath, resolvedTarget, ev, err := r.Resolve(root, "data/file")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if matchedPath != "data/file" {
		t.Errorf("matchedPath = %q, want %q", matchedPath, "data/file")
	}
	if !resolvedTarget.Equals(target) {
		t.Errorf("target = %v, want %v", resolvedTarget, target)
	}

	// Verify evidence
	valid, err := r.Verify(root, matchedPath, resolvedTarget, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Error("Verify should return true")
	}
}
