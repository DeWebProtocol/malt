package resolver_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/resolver/step/implicit"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
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

func newSemantic(t *testing.T, e *overwrite.EAT) mapping.Semantic {
	t.Helper()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	semantic, err := mappingradix.NewMap(scheme, e)
	if err != nil {
		t.Fatalf("radix.NewMap failed: %v", err)
	}
	return semantic
}

func commitStructure(t *testing.T, ctx context.Context, semantic mapping.Semantic, e *overwrite.EAT, bucketID string, arcs map[string]cid.Cid) cid.Cid {
	t.Helper()
	root, err := semantic.Commit(ctx, bucketID, mapping.NewViewFrom(arcs))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if err := e.Update(ctx, bucketID, root, cid.Undef, arcs); err != nil {
		t.Fatalf("EAT.Update failed: %v", err)
	}
	return root
}

const testBucketId = "test-graph"

func TestGatewayExplicitOnly(t *testing.T) {
	e := newTestEAT()
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	ctx := context.Background()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))

	arcsMap := map[string]cid.Cid{
		"a":     k1,
		"a/b":   k2,
		"a/b/c": k3,
	}
	root := commitStructure(t, ctx, semantic, e, testBucketId, arcsMap)

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

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
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	ctx := context.Background()
	target, _ := newPayloadCID([]byte("target"))
	arcsMap := map[string]cid.Cid{
		"a/b": target,
	}
	root := commitStructure(t, ctx, semantic, e, testBucketId, arcsMap)

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
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
	e := newTestEAT()
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	ctx := context.Background()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))

	arcsMap := map[string]cid.Cid{
		"a":     k1,
		"a/b":   k2,
		"a/b/c": k3,
	}
	root := commitStructure(t, ctx, semantic, e, testBucketId, arcsMap)

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	result, err := g.Resolve(root, "a/b/c/d")
	if err == nil && !result.Target.Equals(k3) {
		t.Errorf("Resolve(a/b/c/d) = %v, want %v", result.Target, k3)
	}
}

func TestGatewayImplicitStep(t *testing.T) {
	e := newTestEAT()
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	ctx := context.Background()
	payloadCID, _ := newPayloadCID([]byte("raw-block-data"))
	arcsMap := map[string]cid.Cid{"data": payloadCID}
	root := commitStructure(t, ctx, semantic, e, testBucketId, arcsMap)

	c.AddBlock(payloadCID, []byte("raw-block-data"))

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	result, err := g.Resolve(root, "data")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !result.Target.Defined() {
		t.Error("Target should be defined")
	}
	if len(result.Transcript.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(result.Transcript.Steps))
	}
}

func TestGatewayTranscript(t *testing.T) {
	e := newTestEAT()
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	ctx := context.Background()
	innerCID, _ := newPayloadCID([]byte("inner"))
	outerCID, _ := newPayloadCID([]byte("outer"))

	arcsMap := map[string]cid.Cid{
		"inner": innerCID,
		"outer": outerCID,
	}
	root := commitStructure(t, ctx, semantic, e, testBucketId, arcsMap)

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

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
	e := newTestEAT()
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	ctx := context.Background()
	payloadCID, _ := newPayloadCID([]byte("payload-data"))
	arcsMap := map[string]cid.Cid{
		"@payload": payloadCID,
		"link":     payloadCID,
	}
	root := commitStructure(t, ctx, semantic, e, testBucketId, arcsMap)

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	result, err := g.Resolve(root, "")
	if err != nil {
		t.Fatalf("Resolve with empty path failed: %v", err)
	}
	if !result.Target.Equals(payloadCID) {
		t.Errorf("Empty path resolve target = %v, want payloadCID %v", result.Target, payloadCID)
	}
	if len(result.Transcript.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(result.Transcript.Steps))
	}
	if result.Transcript.Steps[0].Path != "@payload" {
		t.Errorf("Step path = %s, want @payload", result.Transcript.Steps[0].Path)
	}

	valid, err := g.VerifyTranscript(root, result.Transcript)
	if err != nil {
		t.Fatalf("VerifyTranscript failed: %v", err)
	}
	if !valid {
		t.Error("Transcript should be valid")
	}
}

func TestGatewayStructureOnlyNode(t *testing.T) {
	e := newTestEAT()
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	ctx := context.Background()
	targetCID, _ := newPayloadCID([]byte("target-data"))
	arcsMap := map[string]cid.Cid{"link": targetCID}
	root := commitStructure(t, ctx, semantic, e, testBucketId, arcsMap)

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	result, err := g.Resolve(root, "")
	if err != nil {
		t.Fatalf("Resolve with empty path failed: %v", err)
	}
	if !result.Target.Equals(root) {
		t.Errorf("Empty path resolve target = %v, want structure root %v", result.Target, root)
	}
	if len(result.Transcript.Steps) != 0 {
		t.Errorf("Expected 0 steps for structure-only node, got %d", len(result.Transcript.Steps))
	}
}

func TestGatewayNonMaltEmptyPath(t *testing.T) {
	e := newTestEAT()
	semantic := newSemantic(t, e)
	c := mock.NewCAS()

	payloadCID, _ := newPayloadCID([]byte("raw-data"))
	c.AddBlock(payloadCID, []byte("raw-data"))

	explicitR := explicit.NewResolver(e, semantic, testBucketId)
	implicitR := implicit.NewResolver(c)
	g := resolver.NewResolver(explicitR, implicitR)

	result, err := g.Resolve(payloadCID, "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if !result.Target.Equals(payloadCID) {
		t.Errorf("Target = %v, want %v", result.Target, payloadCID)
	}
	if len(result.Transcript.Steps) != 0 {
		t.Errorf("Expected 0 steps, got %d", len(result.Transcript.Steps))
	}
}
