package runtimegraph

import (
	"testing"

	materialmemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
)

func TestNewGraphInitializesSDKComposition(t *testing.T) {
	store := materialmemory.New(true)
	g, err := NewGraph("composition", store, WithNamespace("ns"))
	if err != nil {
		t.Fatalf("NewGraph failed: %v", err)
	}

	if g.ID() != "composition" {
		t.Fatalf("ID = %q, want composition", g.ID())
	}
	if g.Namespace() != "ns" {
		t.Fatalf("Namespace = %q, want ns", g.Namespace())
	}
	if g.Resolver() == nil || g.Writer() == nil {
		t.Fatal("resolver and writer must be initialized")
	}
	if g.Semantic() == nil || g.ListSemantic() == nil {
		t.Fatal("semantic implementations must be initialized")
	}
}
