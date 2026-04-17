// Package sce defines the Structure Commitment Engine.
// SCE coordinates arc set management and delegates to commitment schemes.
package sce

import (
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// Engine is the Structure Commitment Engine.
// It manages arc sets and delegates cryptographic operations to commitment schemes.
// The engine is stateless — each scheme manages its own caching internally.
type Engine struct {
	scheme commitment.Scheme
}

// NewEngine creates a new SCE with the given commitment scheme.
func NewEngine(scheme commitment.Scheme) *Engine {
	return &Engine{
		scheme: scheme,
	}
}

// Scheme returns the underlying commitment scheme.
func (e *Engine) Scheme() commitment.Scheme {
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

	comm, err := e.scheme.Commit(arcs)
	if err != nil {
		logger.Error("SCE.Commit scheme failed",
			logger.Err(err))
		return cid.Cid{}, err
	}

	logger.Info("SCE.Commit completed",
		logger.String("root", comm.String()),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return comm, nil
}

// Prove generates a proof for an arc.
func (e *Engine) Prove(root cid.Cid, arcs arcset.ArcSet, path string) (cid.Cid, []byte, error) {
	start := time.Now()

	logger.Debug("SCE.Prove started",
		logger.String("root", root.String()),
		logger.String("path", path))

	target, proof, err := e.scheme.Prove(root, arcs, path)
	if err != nil {
		logger.Error("SCE.Prove scheme failed",
			logger.String("root", root.String()),
			logger.String("path", path),
			logger.Err(err))
		return cid.Cid{}, nil, err
	}

	logger.Debug("SCE.Prove completed",
		logger.String("root", root.String()),
		logger.String("path", path),
		logger.Int("proof_size", len(proof)),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return target, proof, nil
}

// Verify verifies a proof.
func (e *Engine) Verify(root cid.Cid, path string, target cid.Cid, proof []byte) (bool, error) {
	start := time.Now()

	logger.Debug("SCE.Verify started",
		logger.String("root", root.String()),
		logger.String("path", path),
		logger.Int("proof_size", len(proof)))

	valid, err := e.scheme.Verify(root, path, target, proof)
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

	newComm, err := e.scheme.Update(root, arcs, path, oldKey, newKey)
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
	return e.scheme.BatchUpdate(root, arcs, updates)
}

// BatchProve generates proofs for multiple paths.
func (e *Engine) BatchProve(root cid.Cid, arcs arcset.ArcSet, paths []string) (map[string]arcset.BatchProofEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}
	return e.scheme.BatchProve(root, arcs, paths)
}

// BatchVerify verifies multiple proofs.
func (e *Engine) BatchVerify(root cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return e.scheme.BatchVerify(root, proofs)
}

// AggregateProve generates an aggregated proof.
func (e *Engine) AggregateProve(root cid.Cid, arcs arcset.ArcSet, paths []string) (*arcset.AggregatedProof, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}
	return e.scheme.AggregateProve(root, arcs, paths)
}

// AggregateVerify verifies an aggregated proof.
func (e *Engine) AggregateVerify(root cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	return e.scheme.AggregateVerify(root, aggProof)
}