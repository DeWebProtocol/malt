// Package resolver implements the Explicit Arc Resolver (EAR) for MALT.
// EAR is the core component that resolves structural relationships and
// generates verifiable proofs.
package resolver

import (
	"fmt"

	"github.com/dewebprotocol/malt/pkg/cas"
	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/storage"
	"github.com/dewebprotocol/malt/pkg/types"
)

// EAR is the Explicit Arc Resolver.
// It coordinates between the Structure Commitment Engine (SCE) and
// the Explicit Arc Table (EAT) to provide verifiable arc resolution.
type EAR struct {
	// sce is the Structure Commitment Engine
	sce commitment.CommitmentScheme

	// eat is the Explicit Arc Table storage
	eat storage.LineageStore

	// cas is the Content-Addressed Storage
	cas cas.CAS

	// config holds the resolver configuration
	config *Config
}

// Config holds EAR configuration.
type Config struct {
	// EnableCaching enables caching of resolved arcs
	EnableCaching bool

	// MaxLineageDepth limits the maximum lineage traversal depth
	MaxLineageDepth int
}

// DefaultConfig returns the default EAR configuration.
func DefaultConfig() *Config {
	return &Config{
		EnableCaching:   true,
		MaxLineageDepth: 100,
	}
}

// New creates a new Explicit Arc Resolver.
func New(sce commitment.CommitmentScheme, eat storage.LineageStore, casStorage cas.CAS, cfg *Config) (*EAR, error) {
	if sce == nil {
		return nil, fmt.Errorf("commitment scheme is required")
	}
	if eat == nil {
		return nil, fmt.Errorf("storage is required")
	}
	// CAS is optional - it's only needed if the resolver needs to fetch objects

	if cfg == nil {
		cfg = DefaultConfig()
	}

	return &EAR{
		sce:    sce,
		eat:    eat,
		cas:    casStorage,
		config: cfg,
	}, nil
}

// ResolveResult contains the result of a resolution operation.
type ResolveResult struct {
	// Target is the resolved target CID
	Target types.CID

	// Proof is the cryptographic proof of the arc
	Proof commitment.Proof

	// Commitment is the commitment used (may differ from input for versioned resolution)
	Commitment commitment.Commitment
}

// Resolve resolves a path from a commitment.
// It implements the resolution procedure from Section 4.3:
// 1. Look up in EAT: (C_v, p) -> c
// 2. Generate proof: π = Prove(C_v, (p, c))
// 3. Return (c, π)
//
// If the entry is not found under the given commitment, it attempts
// versioned resolution by traversing the commitment lineage.
func (e *EAR) Resolve(comm commitment.Commitment, p types.Path, arcs *types.ArcSet) (*ResolveResult, error) {
	// Step 1: Look up in EAT
	target, err := e.eat.Get(comm, p)
	if err != nil {
		if storage.IsNotFound(err) {
			// Try versioned resolution
			return e.versionedResolve(comm, p, arcs)
		}
		return nil, fmt.Errorf("EAT lookup failed: %w", err)
	}

	// Step 2: Generate proof
	_, proof, err := e.sce.Prove(comm, arcs, p)
	if err != nil {
		return nil, fmt.Errorf("proof generation failed: %w", err)
	}

	return &ResolveResult{
		Target:     target,
		Proof:      proof,
		Commitment: comm,
	}, nil
}

// ResolveSimple resolves a path and returns just the target and proof.
func (e *EAR) ResolveSimple(comm commitment.Commitment, p types.Path, arcs *types.ArcSet) (types.CID, commitment.Proof, error) {
	result, err := e.Resolve(comm, p, arcs)
	if err != nil {
		return types.CID{}, nil, err
	}
	return result.Target, result.Proof, nil
}

// versionedResolve implements versioned resolution from Section 4.4.
// It traverses the commitment lineage to find the entry.
func (e *EAR) versionedResolve(comm commitment.Commitment, p types.Path, arcs *types.ArcSet) (*ResolveResult, error) {
	// Get the full lineage
	lineage, err := e.eat.GetLineage(comm)
	if err != nil {
		return nil, fmt.Errorf("failed to get lineage: %w", err)
	}

	// Limit traversal depth
	if len(lineage) > e.config.MaxLineageDepth {
		return nil, fmt.Errorf("lineage depth exceeds maximum (%d > %d)",
			len(lineage), e.config.MaxLineageDepth)
	}

	// Traverse lineage from newest to oldest
	for _, ancestorComm := range lineage {
		target, err := e.eat.Get(ancestorComm, p)
		if err == nil {
			// Found the entry
			// Generate proof from the latest commitment
			_, proof, err := e.sce.Prove(comm, arcs, p)
			if err != nil {
				return nil, fmt.Errorf("proof generation failed: %w", err)
			}

			return &ResolveResult{
				Target:     target,
				Proof:      proof,
				Commitment: ancestorComm,
			}, nil
		}
	}

	return nil, fmt.Errorf("path %s not found in commitment lineage", p)
}

// UpdateResult contains the result of an update operation.
type UpdateResult struct {
	// NewCommitment is the new commitment after the update
	NewCommitment commitment.Commitment

	// OldCommitment is the commitment before the update
	OldCommitment commitment.Commitment

	// Path is the updated path
	Path types.Path

	// OldTarget is the previous target CID
	OldTarget types.CID

	// NewTarget is the new target CID
	NewTarget types.CID
}

// Update updates an arc and returns the new commitment.
// This implements localized structural updates without ancestor propagation.
func (e *EAR) Update(comm commitment.Commitment, p types.Path, newCID types.CID, arcs *types.ArcSet) (*UpdateResult, error) {
	// Get the old target from the arc set
	oldCID, ok := arcs.Get(p)
	if !ok {
		return nil, fmt.Errorf("path %s not found in arc set", p)
	}

	// Update the commitment (O(1) operation)
	newComm, err := e.sce.Update(comm, p, oldCID, newCID)
	if err != nil {
		return nil, fmt.Errorf("commitment update failed: %w", err)
	}

	// Update EAT with new entry
	if err := e.eat.Put(newComm, p, newCID); err != nil {
		return nil, fmt.Errorf("EAT update failed: %w", err)
	}

	// Record lineage
	if err := e.eat.SetParent(newComm, comm); err != nil {
		return nil, fmt.Errorf("lineage recording failed: %w", err)
	}

	// Update the arc set
	arcs.Add(p, newCID)

	return &UpdateResult{
		NewCommitment: newComm,
		OldCommitment: comm,
		Path:          p,
		OldTarget:     oldCID,
		NewTarget:     newCID,
	}, nil
}

// AddArc adds a new arc to a structure.
func (e *EAR) AddArc(comm commitment.Commitment, p types.Path, target types.CID, arcs *types.ArcSet) (commitment.Commitment, error) {
	// Check if arc already exists
	if _, err := e.eat.Get(comm, p); err == nil {
		return nil, fmt.Errorf("arc with path %s already exists", p)
	}

	// Add to arc set
	arcs.Add(p, target)

	// Create new commitment
	newComm, err := e.sce.Commit(arcs)
	if err != nil {
		return nil, fmt.Errorf("commitment failed: %w", err)
	}

	// Update EAT
	if err := e.eat.Put(newComm, p, target); err != nil {
		return nil, fmt.Errorf("EAT update failed: %w", err)
	}

	// Record lineage
	if err := e.eat.SetParent(newComm, comm); err != nil {
		return nil, fmt.Errorf("lineage recording failed: %w", err)
	}

	return newComm, nil
}

// RemoveArc removes an arc from a structure.
func (e *EAR) RemoveArc(comm commitment.Commitment, p types.Path, arcs *types.ArcSet) (commitment.Commitment, error) {
	// Check if arc exists
	if _, err := e.eat.Get(comm, p); err != nil {
		return nil, fmt.Errorf("arc with path %s does not exist", p)
	}

	// Remove from arc set
	arcs.Remove(p)

	// Create new commitment
	newComm, err := e.sce.Commit(arcs)
	if err != nil {
		return nil, fmt.Errorf("commitment failed: %w", err)
	}

	// Record lineage
	if err := e.eat.SetParent(newComm, comm); err != nil {
		return nil, fmt.Errorf("lineage recording failed: %w", err)
	}

	return newComm, nil
}

// Verify verifies a proof for a given arc.
func (e *EAR) Verify(comm commitment.Commitment, p types.Path, c types.CID, proof commitment.Proof) (bool, error) {
	return e.sce.Verify(comm, p, c, proof)
}

// BatchUpdate updates multiple arcs in a single operation.
func (e *EAR) BatchUpdate(comm commitment.Commitment, updates map[types.Path]types.CID, arcs *types.ArcSet) (commitment.Commitment, error) {
	// Prepare batch updates with old CID verification
	schemeUpdates := make(map[types.Path]struct {
		Old types.CID
		New types.CID
	})

	for p, newCID := range updates {
		oldCID, err := e.eat.Get(comm, p)
		if err != nil {
			return nil, fmt.Errorf("failed to get old target for path %s: %w", p, err)
		}
		schemeUpdates[p] = struct {
			Old types.CID
			New types.CID
		}{Old: oldCID, New: newCID}
	}

	// Batch update commitment
	newComm, err := e.sce.BatchUpdate(comm, schemeUpdates)
	if err != nil {
		return nil, fmt.Errorf("batch commitment update failed: %w", err)
	}

	// Batch update EAT
	var ops []storage.Operation
	for p, newCID := range updates {
		ops = append(ops, storage.Operation{
			Type: storage.OpPut,
			Entry: storage.EATEntry{
				Commitment: newComm,
				Path:       p,
				Target:     newCID,
			},
		})
	}
	if err := e.eat.Batch(ops); err != nil {
		return nil, fmt.Errorf("EAT batch update failed: %w", err)
	}

	// Record lineage
	if err := e.eat.SetParent(newComm, comm); err != nil {
		return nil, fmt.Errorf("lineage recording failed: %w", err)
	}

	// Update arc set
	for p, newCID := range updates {
		arcs.Add(p, newCID)
	}

	return newComm, nil
}

// GetLineage returns the commitment lineage for a given commitment.
func (e *EAR) GetLineage(comm commitment.Commitment) ([]commitment.Commitment, error) {
	return e.eat.GetLineage(comm)
}

// GetLatestCommitment returns the latest commitment in a lineage.
func (e *EAR) GetLatestCommitment(rootComm commitment.Commitment) (commitment.Commitment, error) {
	return e.eat.GetLatest(rootComm)
}