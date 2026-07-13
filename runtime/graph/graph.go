// Package runtimegraph provides ArcTable-backed graph runtime wiring for MALT.
// Runtime graph values do not own authoritative heads or freshness policy.
package runtimegraph

import (
	"context"
	"fmt"

	malt "github.com/dewebprotocol/malt"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/graph/resolver/step/explicit"
	"github.com/dewebprotocol/malt/graph/writer"
	"github.com/dewebprotocol/malt/runtime/arctable"
	listtree "github.com/dewebprotocol/malt/runtime/semantic/list/tree"
	mappingradix "github.com/dewebprotocol/malt/runtime/semantic/mapping/radix"
)

// RuntimeGraph is a per-graph runtime composition of semantic implementations,
// resolver, and writer. It does not own authoritative heads or freshness policy.
// The root CID is always supplied by callers.
type RuntimeGraph struct {
	id           string
	namespace    string
	semantic     mapping.Semantics
	listSemantic list.Semantics
	resolver     graph.Resolver
	wr           graph.Writer
}

// NewGraph creates a new per-graph instance with its own semantic layer,
// resolver, and writer.
//
// Parameters:
//   - id: unique graph identifier
//   - arctable: shared ArcTable (namespace by namespace) — from Node
//   - opts: functional options (WithCommitmentScheme, WithNamespace, etc.)
func NewGraph(id string, arctable arctable.ArcTable, opts ...Option) (*RuntimeGraph, error) {
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

	// Create per-graph resolver with explicit native MALT resolution.
	res := resolver.NewResolver(explicitStep)

	// Create per-graph writer
	wr := writer.NewWriter(semantic, arctable, listSemantic)

	return &RuntimeGraph{
		id:           id,
		namespace:    namespace,
		semantic:     semantic,
		listSemantic: listSemantic,
		resolver:     res,
		wr:           wr,
	}, nil
}

// ID returns the graph identifier.
func (g *RuntimeGraph) ID() string {
	return g.id
}

// Namespace returns the ArcTable namespace for this graph.
func (g *RuntimeGraph) Namespace() string {
	return g.namespace
}

// Semantic returns the per-graph keyed-map semantic.
func (g *RuntimeGraph) Semantic() mapping.Semantics {
	return g.semantic
}

// ListSemantic returns the per-graph list semantic.
func (g *RuntimeGraph) ListSemantic() list.Semantics {
	return g.listSemantic
}

// Resolver returns the per-graph resolver.
func (g *RuntimeGraph) Resolver() graph.Resolver {
	return g.resolver
}

// Resolve adapts the reference graph resolver transcript to the portable core
// result consumed by execution.Executor and malt.VerifyResolve.
func (g *RuntimeGraph) Resolve(ctx context.Context, req malt.ResolveRequest) (malt.ResolveResult, error) {
	if g == nil {
		return malt.ResolveResult{}, fmt.Errorf("runtime graph is nil")
	}
	if err := req.Validate(); err != nil {
		return malt.ResolveResult{}, err
	}
	path, _ := malt.NewSegmentPath(req.Segments)
	if path.Empty() {
		return malt.ResolveResult{
			Target:    req.Root,
			ProofList: prooflist.ProofList{Root: req.Root, Query: "", Steps: []prooflist.Step{}},
		}, nil
	}
	resolved, err := g.resolver.ResolveKey(ctx, req.Root, path.String())
	if err != nil {
		return malt.ResolveResult{}, err
	}
	if resolved == nil || !resolved.RemainingPath.IsEmpty() || !resolved.Target.Defined() {
		return malt.ResolveResult{}, malt.ErrQueryNotFound
	}
	pl, err := resolver.ProofListFromTranscript(req.Root, resolved.Transcript)
	if err != nil {
		return malt.ResolveResult{}, err
	}
	pl.Query = path.String()
	return malt.ResolveResult{Target: resolved.Target, ProofList: *pl}, nil
}

// Writer returns the per-graph writer.
func (g *RuntimeGraph) Writer() graph.Writer {
	return g.wr
}
