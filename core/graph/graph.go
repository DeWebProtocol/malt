// Package graph provides the graph-scoped unit for MALT. Graph combines read
// (Resolver) and write (Writer) capabilities around explicit arc state.
package graph

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/resolver/step/implicit"
	"github.com/dewebprotocol/malt/core/structure/list"
	listtree "github.com/dewebprotocol/malt/core/structure/list/tree"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/writer"
	cid "github.com/ipfs/go-cid"
)

// Graph is a per-graph unit combining resolver (read) and writer (write).
// It is stateless: the root CID is always passed as a parameter, never held internally.
type Graph struct {
	id              string
	namespace       string
	scheme          commitment.IndexCommitment
	semantic        mapping.Semantics
	listSemantic    list.Semantics
	resolver        *resolver.Resolver
	wr              *writer.Writer
	arctable        arctable.ArcTable
	lineageRecorder writer.LineageRecorder
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
	wr := writer.NewWriter(semantic, arctable, o.LineageRecorder)

	return &Graph{
		id:              id,
		namespace:       namespace,
		scheme:          scheme,
		semantic:        semantic,
		listSemantic:    listSemantic,
		resolver:        res,
		wr:              wr,
		arctable:        arctable,
		lineageRecorder: o.LineageRecorder,
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

// Resolve resolves a path from a root and returns the target plus a proof.
func (g *Graph) Resolve(ctx context.Context, root cid.Cid, path string) (cid.Cid, Proof, error) {
	if !root.Defined() {
		return cid.Cid{}, nil, fmt.Errorf("root must be defined")
	}
	result, err := g.resolver.Resolve(root, path)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("resolution failed: %w", err)
	}
	if !result.RemainingPath.IsEmpty() {
		return cid.Cid{}, nil, fmt.Errorf("resolution incomplete: remaining path %q", result.RemainingPath.String())
	}
	return result.Target, NewTranscriptProof(result.Transcript), nil
}

// BatchResolve resolves multiple paths from a root.
func (g *Graph) BatchResolve(ctx context.Context, root cid.Cid, paths []string) (map[string]cid.Cid, *arcset.AggregatedProof, error) {
	if !root.Defined() {
		return nil, nil, fmt.Errorf("root must be defined")
	}
	results := make(map[string]cid.Cid)
	for _, p := range paths {
		result, err := g.resolver.Resolve(root, p)
		if err != nil {
			continue
		}
		if !result.RemainingPath.IsEmpty() {
			continue
		}
		results[p] = result.Target
	}
	return results, nil, nil
}

// Verify verifies a proof against a root and expected target.
func (g *Graph) Verify(ctx context.Context, root cid.Cid, proof Proof, expectedTarget cid.Cid) (bool, error) {
	if !root.Defined() {
		return false, fmt.Errorf("root must be defined")
	}
	return proof.Verify(root, expectedTarget)
}

// Update applies a batch of arc updates under a root.
func (g *Graph) Update(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *UpdateDelta, error) {
	if !root.Defined() {
		return cid.Cid{}, nil, fmt.Errorf("root must be defined")
	}
	result, err := g.wr.BatchUpdateArcs(ctx, g.namespace, root, arcs)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("batch update failed: %w", err)
	}
	delta := &UpdateDelta{
		OldRoot:              result.OldRoot,
		NewRoot:              result.NewRoot,
		RewriteAmplification: 1.0,
	}
	for _, r := range result.PerArc {
		switch r.Op {
		case writer.ArcInsert:
			delta.Added = append(delta.Added, r.Path.String())
		case writer.ArcReplace:
			delta.Updated = append(delta.Updated, r.Path.String())
		case writer.ArcDelete:
			delta.Deleted = append(delta.Deleted, r.Path.String())
		}
	}
	return result.NewRoot, delta, nil
}

// Snapshot implements GraphWriter.Snapshot.
func (g *Graph) Snapshot(ctx context.Context, root cid.Cid) (arcset.ArcSet, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root must be defined")
	}
	return g.wr.GetSnapshot(ctx, g.namespace, root)
}

// Commit implements GraphWriter.Commit.
func (g *Graph) Commit(ctx context.Context, snapshot arcset.ArcSet) (cid.Cid, error) {
	return g.wr.CreateStructure(ctx, g.namespace, snapshot)
}
