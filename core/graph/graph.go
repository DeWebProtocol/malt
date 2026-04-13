// Package graph provides the stateless Graph implementation.
// This is the core MALT abstraction for resolution, update, and verification.
package graph

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/graph/resolver"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// Graph implements the stateless Graph interface.
// It combines resolution (via Resolver), update (via ArcStore + Commitment),
// and verification (via CommitmentBackend).
type Graph struct {
	arcStore     interfaces.ArcStore
	contentStore interfaces.ContentStore
	backend      interfaces.CommitmentBackend
	resolver     resolver.Resolver
}

// NewGraph creates a new stateless Graph with the given components.
func NewGraph(
	arcStore interfaces.ArcStore,
	contentStore interfaces.ContentStore,
	backend interfaces.CommitmentBackend,
	resolver resolver.Resolver,
) *Graph {
	return &Graph{
		arcStore:     arcStore,
		contentStore: contentStore,
		backend:      backend,
		resolver:     resolver,
	}
}

// Resolve resolves a path from a root CID, returning the target and proof.
func (g *Graph) Resolve(ctx context.Context, root cid.Cid, path string) (cid.Cid, interfaces.Proof, error) {
	if !root.Defined() {
		return cid.Cid{}, nil, fmt.Errorf("root must be defined")
	}

	result, err := g.resolver.Resolve(ctx, root, path)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("resolution failed: %w", err)
	}

	// Convert transcript to Proof
	// The resolver provides verification capability
	proof := &TranscriptProof{
		transcript: result.Transcript,
	}

	return result.Target, proof, nil
}

// BatchResolve resolves multiple paths from a root CID.
func (g *Graph) BatchResolve(ctx context.Context, root cid.Cid, paths []string) (map[string]cid.Cid, *interfaces.AggregatedProof, error) {
	if !root.Defined() {
		return nil, nil, fmt.Errorf("root must be defined")
	}

	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("paths must not be empty")
	}

	// Get snapshot for commitment operations
	snapshot, err := g.arcStore.Snapshot(ctx, root)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get snapshot: %w", err)
	}

	// Resolve each path individually (can be optimized later)
	results := make(map[string]cid.Cid)
	for _, path := range paths {
		result, err := g.resolver.Resolve(ctx, root, path)
		if err != nil {
			// Skip failed resolutions
			continue
		}
		results[path] = result.Target
	}

	// Generate aggregated proof
	aggProof, err := g.backend.AggregateProve(root, snapshot, paths)
	if err != nil {
		// If aggregated proof fails, return individual results without proof
		return results, nil, nil
	}

	// Wrap aggregated proof
	wrappedProof := &interfaces.AggregatedProof{
		Commitment: root,
		Results:    results,
		ProofData:  aggProof.ProofData,
		Backend:    g.backend.Name(),
	}

	return results, wrappedProof, nil
}

// Update updates arcs under a root, returning the new root and deltas.
func (g *Graph) Update(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *interfaces.UpdateDelta, error) {
	if !root.Defined() {
		return cid.Cid{}, nil, fmt.Errorf("root must be defined")
	}

	// Get current snapshot
	snapshot, err := g.arcStore.Snapshot(ctx, root)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to get snapshot: %w", err)
	}

	// Prepare update delta
	delta := &interfaces.UpdateDelta{
		OldRoot:              root,
		Added:                []string{},
		Updated:              []string{},
		Deleted:              []string{},
		RewriteAmplification: 1.0, // MALT always has rewrite amp = 1
	}

	// Prepare batch update for backend
	updates := make(map[string]struct {
		Old cid.Cid
		New cid.Cid
	})

	for path, newTarget := range arcs {
		oldTarget, exists := snapshot.Get(path)

		if newTarget == cid.Undef {
			// Delete operation
			if exists {
				delta.Deleted = append(delta.Deleted, path)
				updates[path] = struct {
					Old cid.Cid
					New cid.Cid
				}{Old: oldTarget, New: cid.Undef}
			}
		} else if !exists {
			// Add operation
			delta.Added = append(delta.Added, path)
			updates[path] = struct {
				Old cid.Cid
				New cid.Cid
			}{Old: cid.Undef, New: newTarget}
		} else if oldTarget != newTarget {
			// Update operation
			delta.Updated = append(delta.Updated, path)
			updates[path] = struct {
				Old cid.Cid
				New cid.Cid
			}{Old: oldTarget, New: newTarget}
		}
	}

	// Apply updates to arc store first (will be under new root)
	// Generate new commitment
	newRoot, err := g.backend.BatchUpdate(root, snapshot, updates)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to update commitment: %w", err)
	}

	// Store arcs under new root
	if err := g.arcStore.BatchPut(ctx, newRoot, arcs); err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to store arcs: %w", err)
	}

	delta.NewRoot = newRoot
	return newRoot, delta, nil
}

// BatchUpdate is a synonym for Update (Update already supports batch).
func (g *Graph) BatchUpdate(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *interfaces.UpdateDelta, error) {
	return g.Update(ctx, root, arcs)
}

// Verify verifies a proof against a root and expected target.
func (g *Graph) Verify(ctx context.Context, root cid.Cid, proof interfaces.Proof, expectedTarget cid.Cid) (bool, error) {
	if !root.Defined() {
		return false, fmt.Errorf("root must be defined")
	}

	return proof.Verify(root, expectedTarget)
}

// BatchVerify verifies an aggregated proof against a root.
func (g *Graph) BatchVerify(ctx context.Context, root cid.Cid, aggProof *interfaces.AggregatedProof) (bool, error) {
	if !root.Defined() {
		return false, fmt.Errorf("root must be defined")
	}

	if aggProof == nil {
		return false, fmt.Errorf("proof must not be nil")
	}

	// Convert to arcset.AggregatedProof for backend
	backendProof := &arcset.AggregatedProof{
		Paths:   make([]string, 0, len(aggProof.Results)),
		Targets: make([]cid.Cid, 0, len(aggProof.Results)),
		ProofData: aggProof.ProofData,
	}

	for path, target := range aggProof.Results {
		backendProof.Paths = append(backendProof.Paths, path)
		backendProof.Targets = append(backendProof.Targets, target)
	}

	return g.backend.AggregateVerify(root, backendProof)
}

// Snapshot returns an immutable view of the arc set under a root.
func (g *Graph) Snapshot(ctx context.Context, root cid.Cid) (arcset.Snapshot, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root must be defined")
	}

	return g.arcStore.Snapshot(ctx, root)
}

// Commit generates a new commitment from a snapshot.
func (g *Graph) Commit(ctx context.Context, snapshot arcset.View) (cid.Cid, error) {
	if snapshot == nil {
		return cid.Cid{}, fmt.Errorf("snapshot must not be nil")
	}

	return g.backend.Commit(snapshot)
}

// Ensure Graph implements interfaces.Graph.
var _ interfaces.Graph = (*Graph)(nil)
