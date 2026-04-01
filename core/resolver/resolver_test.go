package resolver_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat/simple"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/key"
)

func TestResolverResolveStep(t *testing.T) {
	// Create components
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

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

	// Create resolver (no CAS needed for single-step resolution)
	r := resolver.NewResolver(e, s)

	// Test longest prefix matching
	tests := []struct {
		path          string
		expectedPath  string
		expectedKey   key.Key
	}{
		{"a", "a", k1},
		{"a/b", "a/b", k2},
		{"a/b/c", "a/b/c", k3},
		{"a/b/c/d", "a/b/c", k3}, // Should resolve to a/b/c (longest prefix)
		{"a/b/x", "a/b", k2},     // Should resolve to a/b
		{"a/x", "a", k1},         // Should resolve to a
	}

	for _, tt := range tests {
		matchedPath, target, proof, err := r.ResolveStep(root, tt.path)
		if err != nil {
			t.Errorf("ResolveStep(%s) failed: %v", tt.path, err)
			continue
		}

		if matchedPath != tt.expectedPath {
			t.Errorf("ResolveStep(%s) matchedPath = %s, want %s", tt.path, matchedPath, tt.expectedPath)
		}

		if !target.Equals(tt.expectedKey) {
			t.Errorf("ResolveStep(%s) target = %v, want %v", tt.path, target, tt.expectedKey)
		}

		if len(proof) == 0 {
			t.Errorf("ResolveStep(%s) should generate proof", tt.path)
		}
	}
}

func TestResolverVerifyStep(t *testing.T) {
	// Create components
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create arc set
	arcs := arcset.NewMap()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("a", k1)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store in EAT
	e.Put(root, "a", k1)

	// Create resolver
	r := resolver.NewResolver(e, s)

	// Resolve step
	matchedPath, target, proof, err := r.ResolveStep(root, "a/b/c")
	if err != nil {
		t.Fatalf("ResolveStep failed: %v", err)
	}

	// Verify step
	valid, err := r.VerifyStep(root, matchedPath, target, proof)
	if err != nil {
		t.Fatalf("VerifyStep failed: %v", err)
	}

	if !valid {
		t.Error("VerifyStep should return true")
	}
}

func TestResolverNoMatch(t *testing.T) {
	// Create components
	e := simple.NewEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create arc set
	arcs := arcset.NewMap()
	k1, _ := key.NewPayloadCID([]byte("target1"))
	arcs.Add("x/y/z", k1)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store in EAT
	e.Put(root, "x/y/z", k1)

	// Create resolver
	r := resolver.NewResolver(e, s)

	// Try to resolve non-matching path
	_, _, _, err = r.ResolveStep(root, "a/b/c")
	if err == nil {
		t.Error("ResolveStep should fail for non-matching path")
	}
}