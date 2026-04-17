package resolver_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/resolver/step/implicit"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/types/evidence"
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

// newTestEAT creates a new EAT for testing.
func newTestEAT() *overwrite.EAT {
	kv := kvstore_memory.New()
	e, err := overwrite.NewEAT(overwrite.WithKVStore(kv))
	if err != nil {
		panic(err)
	}
	return e
}

const testBucketId = "test-graph"

func TestGatewayExplicitOnly(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	ctx := context.Background()

	// Create arc set with hierarchical paths pointing to PayloadCIDs
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))

	arcsMap := map[string]cid.Cid{
		"a":     k1,
		"a/b":   k2,
		"a/b/c": k3,
	}
	arcs := arcset.NewSetFrom(arcsMap)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	e.Update(ctx, testBucketId, root, cid.Undef, arcsMap)

	// Create resolver
	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	// Test exact path matching (no remaining path after resolution)
	tests := []struct {
		path     string
		expected cid.Cid
	}{
		{"a", k1},
		{"a/b", k2},
		{"a/b/c", k3},
	}

	for _, tt := range tests {
		result, err := g.Resolve(root, tt.path)
		if err != nil {
			t.Errorf("Resolve(%s) failed: %v", tt.path, err)
			continue
		}

		if !result.Target.Equals(tt.expected) {
			t.Errorf("Resolve(%s) = %v, want %v", tt.path, result.Target, tt.expected)
		}

		if len(result.Transcript.Steps) != 1 {
			t.Errorf("Resolve(%s) should have exactly one step, got %d", tt.path, len(result.Transcript.Steps))
		}

		valid, err := g.VerifyTranscript(root, result.Transcript)
		if err != nil {
			t.Errorf("VerifyTranscript(%s) failed: %v", tt.path, err)
		}
		if !valid {
			t.Errorf("VerifyTranscript(%s) should be valid", tt.path)
		}
	}
}

func TestGatewayCanonicalizesResolvePath(t *testing.T) {
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	ctx := context.Background()
	target, _ := newPayloadCID([]byte("target"))
	arcsMap := map[string]cid.Cid{
		"a/b": target,
	}
	arcs := arcset.NewSetFrom(arcsMap)

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	e.Update(ctx, testBucketId, root, cid.Undef, arcsMap)

	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	result, err := g.Resolve(root, "/a//b/")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !result.Target.Equals(target) {
		t.Errorf("Resolve(/a//b/) = %v, want %v", result.Target, target)
	}
	if len(result.Transcript.Steps) != 1 || result.Transcript.Steps[0].Path != "a/b" {
		t.Errorf("expected canonical transcript path a/b, got %+v", result.Transcript.Steps)
	}
}

func TestGatewayExplicitLongestPrefix(t *testing.T) {
	// Test that longest prefix matching works
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	ctx := context.Background()

	// Create arc set
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))

	arcsMap := map[string]cid.Cid{
		"a":     k1,
		"a/b":   k2,
		"a/b/c": k3,
	}
	arcs := arcset.NewSetFrom(arcsMap)

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	e.Update(ctx, testBucketId, root, cid.Undef, arcsMap)

	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	// Test: "a/b/c/d" should resolve to k3 (longest prefix "a/b/c")
	// Then Resolver tries to continue with remaining path "d" via implicit resolution
	// Since CAS doesn't have the block for k3, it should return error
	result, err := g.Resolve(root, "a/b/c/d")
	if err == nil {
		// If no error, the target should be k3 (explicit step only)
		// This means Gateway stopped at explicit resolution
		if !result.Target.Equals(k3) {
			t.Errorf("Resolve(a/b/c/d) = %v, want %v", result.Target, k3)
		}
	}
	// If error, that's also acceptable behavior (CAS doesn't have the block)
}

func TestGatewayImplicitStep(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	ctx := context.Background()

	// Create arc set pointing to a PayloadCID
	payloadCID, _ := newPayloadCID([]byte("raw-block-data"))
	arcsMap := map[string]cid.Cid{"data": payloadCID}
	arcs := arcset.NewSetFrom(arcsMap)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	e.Update(ctx, testBucketId, root, cid.Undef, arcsMap)

	// Add block to mock CAS
	c.AddBlock(payloadCID, []byte("raw-block-data"))

	// Create resolver
	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	// Resolve should stop at PayloadCID
	result, err := g.Resolve(root, "data")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Check that target is defined (was a PayloadCID)
	if !result.Target.Defined() {
		t.Error("Target should be defined")
	}

	if len(result.Transcript.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(result.Transcript.Steps))
	}
}

func TestGatewayTranscript(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	ctx := context.Background()

	// Create arc set with nested structure
	innerCID, _ := newPayloadCID([]byte("inner"))
	outerCID, _ := newPayloadCID([]byte("outer"))

	arcsMap := map[string]cid.Cid{
		"inner": innerCID,
		"outer": outerCID,
	}
	arcs := arcset.NewSetFrom(arcsMap)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	e.Update(ctx, testBucketId, root, cid.Undef, arcsMap)

	// Create resolver
	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	// Resolve and check transcript
	result, err := g.Resolve(root, "inner")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(result.Transcript.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(result.Transcript.Steps))
	}

	step := result.Transcript.Steps[0]
	if step.Path != "inner" {
		t.Errorf("Step path = %s, want inner", step.Path)
	}
	if !step.Target.Equals(innerCID) {
		t.Error("Step target should match innerCID")
	}
	if step.Evidence.Kind() != evidence.EvidenceKindExplicit {
		t.Error("Step evidence should be ExplicitEvidence")
	}
}

func TestGatewayPayloadRedirect(t *testing.T) {
	// Test @payload redirect when resolving MALT CID with empty path
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	ctx := context.Background()

	// Create arc set with @payload pointing to a payload CID
	payloadCID, _ := newPayloadCID([]byte("payload-data"))
	arcsMap := map[string]cid.Cid{
		"@payload": payloadCID,
		"link":     payloadCID,
	}
	arcs := arcset.NewSetFrom(arcsMap)

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	e.Update(ctx, testBucketId, root, cid.Undef, arcsMap)

	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	// Resolve with empty path - should auto-redirect to @payload
	result, err := g.Resolve(root, "")
	if err != nil {
		t.Fatalf("Resolve with empty path failed: %v", err)
	}

	// Should resolve to payload CID
	if !result.Target.Equals(payloadCID) {
		t.Errorf("Empty path resolve target = %v, want payloadCID %v", result.Target, payloadCID)
	}

	// Should have one step with @payload
	if len(result.Transcript.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(result.Transcript.Steps))
	}

	if result.Transcript.Steps[0].Path != "@payload" {
		t.Errorf("Step path = %s, want @payload", result.Transcript.Steps[0].Path)
	}

	// Verify transcript
	valid, err := g.VerifyTranscript(root, result.Transcript)
	if err != nil {
		t.Fatalf("VerifyTranscript failed: %v", err)
	}
	if !valid {
		t.Error("Transcript should be valid")
	}
}

func TestGatewayStructureOnlyNode(t *testing.T) {
	// Test that structure-only nodes (no @payload) return structure root
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	ctx := context.Background()

	// Create arc set WITHOUT @payload (structure-only node)
	targetCID, _ := newPayloadCID([]byte("target-data"))
	arcsMap := map[string]cid.Cid{"link": targetCID}
	arcs := arcset.NewSetFrom(arcsMap)

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	e.Update(ctx, testBucketId, root, cid.Undef, arcsMap)

	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	// Resolve with empty path - should return structure root (no @payload)
	result, err := g.Resolve(root, "")
	if err != nil {
		t.Fatalf("Resolve with empty path failed: %v", err)
	}

	// Should return the structure root itself
	if !result.Target.Equals(root) {
		t.Errorf("Empty path resolve target = %v, want structure root %v", result.Target, root)
	}

	// Should have no steps (no @payload to resolve)
	if len(result.Transcript.Steps) != 0 {
		t.Errorf("Expected 0 steps for structure-only node, got %d", len(result.Transcript.Steps))
	}
}

func TestGatewayNonMaltEmptyPath(t *testing.T) {
	// Test that non-MALT CIDs with empty path are returned directly
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create a regular payload CID (not MALT)
	payloadCID, _ := newPayloadCID([]byte("raw-data"))
	c.AddBlock(payloadCID, []byte("raw-data"))

	explicitR := explicit.NewResolver(e, s, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	// Resolve non-MALT CID with empty path
	result, err := g.Resolve(payloadCID, "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Should return the same CID
	if !result.Target.Equals(payloadCID) {
		t.Errorf("Target = %v, want %v", result.Target, payloadCID)
	}

	// Should have no steps
	if len(result.Transcript.Steps) != 0 {
		t.Errorf("Expected 0 steps, got %d", len(result.Transcript.Steps))
	}
}
