// Package graph provides the graph-scoped unit for MALT. Graph combines read
// (Resolver) and write (Writer) capabilities around explicit arc state. It is
// runtime composition, not an authoritative head or freshness owner.
package graph

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/resolver/step/implicit"
	"github.com/dewebprotocol/malt/core/structure/list"
	listtree "github.com/dewebprotocol/malt/core/structure/list/tree"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/writer"
)

// Graph is a per-graph unit combining resolver (read) and writer (write).
// It is stateless: the root CID is always passed as a parameter, never held internally.
type Graph struct {
	id           string
	namespace    string
	semantic     mapping.Semantics
	listSemantic list.Semantics
	resolver     *resolver.Resolver
	wr           *writer.Writer
}

// NewGraph creates a new per-graph instance with its own semantic layer,
// resolver, and writer.
//
// Parameters:
//   - id: unique graph identifier
//   - arctable: shared ArcTable (namespace by namespace) — from Node
//   - cas: shared read-side CAS client — from Node (nil for testing/mocks)
//   - opts: functional options (WithCommitmentScheme, WithNamespace, etc.)
func NewGraph(id string, arctable arctable.ArcTable, cas cas.Reader, opts ...Option) (*Graph, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	// Default namespace = graph id
	namespace := o.Namespace
	if namespace == "" {
		namespace = id
	}

	// Default commitment scheme: KZG
	scheme := o.Scheme
	if scheme == nil {
		s, err := kzg.NewScheme()
		if err != nil {
			return nil, fmt.Errorf("failed to create default KZG scheme: %w", err)
		}
		scheme = s
	}

	semantic, err := mappingradix.NewMap(scheme, arctable)
	if err != nil {
		return nil, fmt.Errorf("failed to create mapping semantic: %w", err)
	}
	listSemantic, err := listtree.NewList(scheme, arctable)
	if err != nil {
		return nil, fmt.Errorf("failed to create list semantic: %w", err)
	}

	// Create per-graph explicit resolver
	explicitStep := explicit.NewResolver(arctable, semantic, namespace)

	// Create per-graph implicit resolver
	implicitStep := implicit.NewResolver(cas)

	// Create per-graph resolver with explicit native resolution and optional
	// interoperability steps for legacy CID traversal.
	res := resolver.NewResolver(explicitStep, implicitStep)

	// Create per-graph writer
	wr := writer.NewWriter(semantic, arctable)

	return &Graph{
		id:           id,
		namespace:    namespace,
		semantic:     semantic,
		listSemantic: listSemantic,
		resolver:     res,
		wr:           wr,
	}, nil
}

// ID returns the graph identifier.
func (g *Graph) ID() string {
	return g.id
}

// Namespace returns the ArcTable namespace for this graph.
func (g *Graph) Namespace() string {
	return g.namespace
}

// Semantic returns the per-graph keyed-map semantic.
func (g *Graph) Semantic() mapping.Semantics {
	return g.semantic
}

// ListSemantic returns the per-graph list semantic.
func (g *Graph) ListSemantic() list.Semantics {
	return g.listSemantic
}

// Resolver returns the per-graph resolver.
func (g *Graph) Resolver() *resolver.Resolver {
	return g.resolver
}

// Writer returns the per-graph writer.
func (g *Graph) Writer() *writer.Writer {
	return g.wr
}
