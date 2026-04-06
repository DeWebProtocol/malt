package resolver_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/explicit"
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
	e, err := overwrite.NewEAT(kv)
	if err != nil {
		panic(err)
	}
	return e
}

const testBucketId = "test-graph"

// collectArcs collects arcs from an arcset.Map into a map.
func collectArcs(arcs *arcset.Map) map[string]cid.Cid {
	return arcs.AsMap()
}

func TestExplicitResolverResolve(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create arc set with hierarchical paths
	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	k3, _ := newPayloadCID([]byte("target3"))

	arcs.Set("a", k1)
	arcs.Set("a/b", k2)
	arcs.Set("a/b/c", k3)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store arcs in EAT
	e.Update(testBucketId, root, cid.Undef, collectArcs(arcs))

	// Create explicit resolver
	r := explicit.NewResolver(e, s, testBucketId)

	// Test longest prefix matching
	tests := []struct {
		path          string
		expectedPath  string
		expectedKey   cid.Cid
	}{
		{"a", "a", k1},
		{"a/b", "a/b", k2},
		{"a/b/c", "a/b/c", k3},
		{"a/b/c/d", "a/b/c", k3}, // Should resolve to a/b/c (longest prefix)
		{"a/b/x", "a/b", k2},     // Should resolve to a/b
		{"a/x", "a", k1},         // Should resolve to a
	}

	for _, tt := range tests {
		matchedPath, target, ev, err := r.Resolve(root, tt.path)
		if err != nil {
			t.Errorf("Resolve(%s) failed: %v", tt.path, err)
			continue
		}

		if matchedPath != tt.expectedPath {
			t.Errorf("Resolve(%s) matchedPath = %s, want %s", tt.path, matchedPath, tt.expectedPath)
		}

		if !target.Equals(tt.expectedKey) {
			t.Errorf("Resolve(%s) target = %v, want %v", tt.path, target, tt.expectedKey)
		}

		if ev == nil {
			t.Errorf("Resolve(%s) should return evidence", tt.path)
		}

		if ev != nil && ev.Kind() != evidence.EvidenceKindExplicit {
			t.Errorf("Resolve(%s) evidence kind = %v, want ExplicitEvidence", tt.path, ev.Kind())
		}
	}
}

func TestExplicitResolverVerify(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create arc set
	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Set("a", k1)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store in EAT
	e.Update(testBucketId, root, cid.Undef, collectArcs(arcs))

	// Create explicit resolver
	r := explicit.NewResolver(e, s, testBucketId)

	// Resolve step
	matchedPath, target, ev, err := r.Resolve(root, "a/b/c")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Verify step
	valid, err := r.Verify(root, matchedPath, target, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	if !valid {
		t.Error("Verify should return true")
	}
}

func TestExplicitResolverNoMatch(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	s := sce.NewEngine(scheme)

	// Create arc set
	arcs := arcset.NewMap()
	k1, _ := newPayloadCID([]byte("target1"))
	arcs.Set("x/y/z", k1)

	// Create structure
	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Store in EAT
	e.Update(testBucketId, root, cid.Undef, collectArcs(arcs))

	// Create explicit resolver
	r := explicit.NewResolver(e, s, testBucketId)

	// Try to resolve non-matching path
	_, _, _, err = r.Resolve(root, "a/b/c")
	if err == nil {
		t.Error("Resolve should fail for non-matching path")
	}
}

func TestResolverInterface(t *testing.T) {
	// Verify explicit.Resolver implements resolver.Resolver
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	s := sce.NewEngine(scheme)

	var r resolver.Resolver = explicit.NewResolver(e, s, testBucketId)
	if r == nil {
		t.Error("explicit.Resolver should implement resolver.Resolver")
	}
}