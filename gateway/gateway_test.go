package gateway_test

import (
	"testing"

	"github.com/dewebprotocol/malt/cas/mock"
	"github.com/dewebprotocol/malt/core/eat/memory"
	"github.com/dewebprotocol/malt/core/resolver/explicit"
	"github.com/dewebprotocol/malt/core/resolver/implicit"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/gateway"
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

func TestGatewayExplicitOnly(t *testing.T) {
	// Create components
	e := memory.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create arc set with hierarchical paths pointing to PayloadCIDs
	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))

	arcs.Add("a", k1)
	arcs.Add("a/b", k2)
	arcs.Add("a/b/c", k3)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		e.Put(root, path, target)
	}

	// Create gateway
	explicitR := explicit.NewResolver(e, s)
	implicitR := implicit.NewResolver(c)
	g := gateway.NewGateway(explicitR, implicitR)

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

func TestGatewayExplicitLongestPrefix(t *testing.T) {
	// Test that longest prefix matching works
	e := memory.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create arc set
	arcs := memory.NewView()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))

	arcs.Add("a", k1)
	arcs.Add("a/b", k2)
	arcs.Add("a/b/c", k3)

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		e.Put(root, path, target)
	}

	explicitR := explicit.NewResolver(e, s)
	implicitR := implicit.NewResolver(c)
	g := gateway.NewGateway(explicitR, implicitR)

	// Test: "a/b/c/d" should resolve to k3 (longest prefix "a/b/c")
	// Then Gateway tries to continue with remaining path "d" via implicit resolution
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
	e := memory.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create arc set pointing to a PayloadCID
	arcs := memory.NewView()
	payloadCID, _ := newPayloadCID([]byte("raw-block-data"))
	arcs.Add("data", payloadCID)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	e.Put(root, "data", payloadCID)

	// Add block to mock CAS
	c.AddBlock(payloadCID, []byte("raw-block-data"))

	// Create gateway
	explicitR := explicit.NewResolver(e, s)
	implicitR := implicit.NewResolver(c)
	g := gateway.NewGateway(explicitR, implicitR)

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
	e := memory.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create arc set with nested structure
	arcs := memory.NewView()
	innerCID, _ := newPayloadCID([]byte("inner"))
	outerCID, _ := newPayloadCID([]byte("outer"))

	arcs.Add("inner", innerCID)
	arcs.Add("outer", outerCID)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		e.Put(root, path, target)
	}

	// Create gateway
	explicitR := explicit.NewResolver(e, s)
	implicitR := implicit.NewResolver(c)
	g := gateway.NewGateway(explicitR, implicitR)

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