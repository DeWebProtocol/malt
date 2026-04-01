package resolver_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/cas/mock"
	"github.com/dewebprotocol/malt/core/eat/simple"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/key"
)

func TestResolverExplicitStep(t *testing.T) {
	// Create components
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create arc set with hierarchical paths
	arcs := arcset.NewMap()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	k2, _ := key.NewPayloadCID([]byte("target2"))
	k3, _ := key.NewPayloadCID([]byte("target3"))

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

	// Create resolver
	r := resolver.NewResolver(e, s, c)

	// Test longest prefix matching
	tests := []struct {
		path     string
		expected key.Key
	}{
		{"a", k1},
		{"a/b", k2},
		{"a/b/c", k3},
		{"a/b/c/d", k3}, // Should resolve to a/b/c (longest prefix)
		{"a/b/x", k2},   // Should resolve to a/b
		{"a/x", k1},     // Should resolve to a
	}

	for _, tt := range tests {
		result, err := r.Resolve(root, tt.path)
		if err != nil {
			t.Errorf("Resolve(%s) failed: %v", tt.path, err)
			continue
		}

		if !result.Target.Equals(tt.expected) {
			t.Errorf("Resolve(%s) = %v, want %v", tt.path, result.Target, tt.expected)
		}

		// Verify transcript
		if len(result.Transcript.Steps) == 0 {
			t.Errorf("Resolve(%s) should have at least one step", tt.path)
		}

		// Verify transcript
		valid, err := r.VerifyTranscript(root, result.Transcript)
		if err != nil {
			t.Errorf("VerifyTranscript(%s) failed: %v", tt.path, err)
		}
		if !valid {
			t.Errorf("VerifyTranscript(%s) should be valid", tt.path)
		}
	}
}

func TestResolverImplicitStep(t *testing.T) {
	// Create components
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create arc set pointing to a PayloadCID
	arcs := arcset.NewMap()
	payloadCID, _ := key.NewPayloadCID([]byte("raw-block-data"))
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

	// Create resolver
	r := resolver.NewResolver(e, s, c)

	// Resolve should stop at PayloadCID (implicit step not implemented yet)
	result, err := r.Resolve(root, "data")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Should stop at PayloadCID since implicit traversal requires CAS
	if result.Target.Kind() != key.KeyKindPayloadCID {
		t.Error("Target should be PayloadCID")
	}
}

func TestResolverTranscript(t *testing.T) {
	// Create components
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create arc set with nested structure
	arcs := arcset.NewMap()
	innerCID, _ := key.NewPayloadCID([]byte("inner"))
	outerCID, _ := key.NewPayloadCID([]byte("outer"))

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

	// Create resolver
	r := resolver.NewResolver(e, s, c)

	// Resolve and check transcript
	result, err := r.Resolve(root, "inner")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if len(result.Transcript.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(result.Transcript.Steps))
	}

	step := result.Transcript.Steps[0]
	if step.Kind != resolver.StepExplicit {
		t.Error("Step should be explicit")
	}
	if step.Path != "inner" {
		t.Errorf("Step path = %s, want inner", step.Path)
	}
	if !step.Target.Equals(innerCID) {
		t.Error("Step target should match innerCID")
	}
}