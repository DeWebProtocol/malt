// Package sce defines the Structure Commitment Engine.
// SCE coordinates arc set management and delegates to primitive index
// commitment backends.
package sce

import (
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// Engine is the Structure Commitment Engine.
// It adapts path-addressed arc sets onto a semantic-neutral index commitment
// primitive. Correctness must not depend on RAM-only backend state; any backend
// cache is an optimization only.
type Engine struct {
	scheme commitment.IndexCommitment
}

// NewEngine creates a new SCE with the given index commitment.
func NewEngine(scheme commitment.IndexCommitment) *Engine {
	return &Engine{scheme: scheme}
}

// Scheme returns the underlying index commitment backend.
func (e *Engine) Scheme() commitment.IndexCommitment {
	return e.scheme
}

// Commit generates a commitment to an arc set.
func (e *Engine) Commit(arcs arcset.ArcSet) (cid.Cid, error) {
	start := time.Now()

	logger.Debug("SCE.Commit started")

	if arcs == nil {
		logger.Error("SCE.Commit nil arc set")
		return cid.Cid{}, ErrNilArcSet
	}

	_, cells := commitment.ExtractSortedPathsCells(arcs)
	comm, err := e.scheme.CommitValues(cells)
	if err != nil {
		logger.Error("SCE.Commit scheme failed", logger.Err(err))
		return cid.Cid{}, err
	}

	logger.Info("SCE.Commit completed",
		logger.String("root", comm.String()),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return comm, nil
}

// Prove generates a path-bound proof for an arc.
func (e *Engine) Prove(root cid.Cid, arcs arcset.ArcSet, path string) (cid.Cid, []byte, error) {
	start := time.Now()

	logger.Debug("SCE.Prove started",
		logger.String("root", root.String()),
		logger.String("path", path))

	paths, cells := commitment.ExtractSortedPathsCells(arcs)
	index, ok := commitment.FindPathIndex(paths, path)
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found", path)
	}

	targetCell, proof, err := e.scheme.ProveIndex(root, cells, uint64(index))
	if err != nil {
		logger.Error("SCE.Prove scheme failed",
			logger.String("root", root.String()),
			logger.String("path", path),
			logger.Err(err))
		return cid.Cid{}, nil, err
	}

	target, err := targetCell.AsCID()
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("proved cell at path %s is not a CID: %w", path, err)
	}
	boundProof := wrapPathProof(path, proof)

	logger.Debug("SCE.Prove completed",
		logger.String("root", root.String()),
		logger.String("path", path),
		logger.Int("proof_size", len(boundProof)),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return target, boundProof, nil
}

// Verify verifies a path-bound proof.
func (e *Engine) Verify(root cid.Cid, path string, target cid.Cid, proof []byte) (bool, error) {
	start := time.Now()

	logger.Debug("SCE.Verify started",
		logger.String("root", root.String()),
		logger.String("path", path),
		logger.Int("proof_size", len(proof)))

	primitiveProof, err := unwrapPathProof(path, proof)
	if err != nil {
		return false, err
	}

	valid, err := e.scheme.VerifyProof(root, commitment.CellFromCID(target), primitiveProof)
	if err != nil {
		logger.Error("SCE.Verify failed",
			logger.String("root", root.String()),
			logger.String("path", path),
			logger.Err(err))
		return false, err
	}

	logger.Debug("SCE.Verify completed",
		logger.String("root", root.String()),
		logger.String("path", path),
		logger.Bool("valid", valid),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return valid, nil
}

// Update updates an arc.
func (e *Engine) Update(root cid.Cid, arcs arcset.ArcSet, path string, oldKey, newKey cid.Cid) (cid.Cid, error) {
	start := time.Now()

	logger.Debug("SCE.Update started",
		logger.String("root", root.String()),
		logger.String("path", path),
		logger.String("old_key", oldKey.String()),
		logger.String("new_key", newKey.String()))

	paths, cells := commitment.ExtractSortedPathsCells(arcs)
	index, ok := commitment.FindPathIndex(paths, path)
	if !ok {
		return cid.Cid{}, fmt.Errorf("path %s not found", path)
	}

	newComm, err := e.scheme.ReplaceIndex(root, cells, uint64(index), commitment.CellFromCID(oldKey), commitment.CellFromCID(newKey))
	if err != nil {
		logger.Error("SCE.Update scheme failed",
			logger.String("root", root.String()),
			logger.String("path", path),
			logger.Err(err))
		return cid.Cid{}, err
	}

	logger.Info("SCE.Update completed",
		logger.String("old_root", root.String()),
		logger.String("new_root", newComm.String()),
		logger.String("path", path),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return newComm, nil
}

// BatchUpdate updates multiple arcs.
func (e *Engine) BatchUpdate(root cid.Cid, arcs arcset.ArcSet, updates map[string]struct {
	Old cid.Cid
	New cid.Cid
}) (cid.Cid, error) {
	paths, cells := commitment.ExtractSortedPathsCells(arcs)
	rebuiltRoot, err := e.scheme.CommitValues(cells)
	if err != nil {
		return cid.Cid{}, err
	}
	if !rebuiltRoot.Equals(root) {
		return cid.Cid{}, fmt.Errorf("reconstructed commitment does not match expected root")
	}

	nextCells := commitment.CloneCells(cells)
	for path, update := range updates {
		index, ok := commitment.FindPathIndex(paths, path)
		if !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found", path)
		}
		if !nextCells[index].Equal(commitment.CellFromCID(update.Old)) {
			return cid.Cid{}, fmt.Errorf("old value mismatch for path %s", path)
		}
		nextCells[index] = commitment.CellFromCID(update.New)
	}
	return e.scheme.CommitValues(nextCells)
}

// BatchProve generates proofs for multiple paths.
func (e *Engine) BatchProve(root cid.Cid, arcs arcset.ArcSet, paths []string) (map[string]arcset.BatchProofEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}
	return commitment.BatchProve(paths, func(path string) (cid.Cid, []byte, error) {
		return e.Prove(root, arcs, path)
	})
}

// BatchVerify verifies multiple proofs.
func (e *Engine) BatchVerify(root cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return commitment.BatchVerify(proofs, func(path string, value cid.Cid, proof []byte) (bool, error) {
		return e.Verify(root, path, value, proof)
	})
}

// AggregateProve generates an aggregated proof.
func (e *Engine) AggregateProve(root cid.Cid, arcs arcset.ArcSet, paths []string) (*arcset.AggregatedProof, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}
	return commitment.AggregateProve(paths, func(path string) (cid.Cid, []byte, error) {
		return e.Prove(root, arcs, path)
	})
}

// AggregateVerify verifies an aggregated proof.
func (e *Engine) AggregateVerify(root cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	return commitment.AggregateVerify(aggProof, func(path string, value cid.Cid, proof []byte) (bool, error) {
		return e.Verify(root, path, value, proof)
	})
}
