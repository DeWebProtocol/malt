// Package sce defines the Structure Commitment Engine.
// SCE coordinates arc set management and delegates to commitment schemes.
package sce

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// Engine is the Structure Commitment Engine.
// It manages arc sets and delegates cryptographic operations to commitment schemes.
type Engine struct {
	scheme commitment.Scheme

	mu       sync.RWMutex
	sessions map[string]*session // commitment bytes -> session
}

type session struct {
	paths        []string
	values       []cid.Cid
	pathToIndex  map[string]int
}

// NewEngine creates a new SCE with the given commitment scheme.
func NewEngine(scheme commitment.Scheme) *Engine {
	return &Engine{
		scheme:   scheme,
		sessions: make(map[string]*session),
	}
}

// Scheme returns the underlying commitment scheme.
func (e *Engine) Scheme() commitment.Scheme {
	return e.scheme
}

// Commit generates a commitment to an arc set.
func (e *Engine) Commit(arcs arcset.View) (cid.Cid, error) {
	start := time.Now()

	logger.Debug("SCE.Commit started")

	if arcs == nil {
		logger.Error("SCE.Commit nil arc set")
		return cid.Cid{}, ErrNilArcSet
	}

	// Delegate to commitment scheme
	comm, err := e.scheme.Commit(arcs)
	if err != nil {
		logger.Error("SCE.Commit scheme failed",
			logger.Err(err))
		return cid.Cid{}, err
	}

	// Extract and store session data
	paths, values, pathToIndex, err := extractArcs(arcs)
	if err != nil {
		logger.Error("SCE.Commit extract arcs failed",
			logger.Err(err))
		return cid.Cid{}, err
	}

	// Extract commitment bytes for session key
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		logger.Error("SCE.Commit extract commitment failed",
			logger.Err(err))
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	e.mu.Lock()
	e.sessions[string(commBytes)] = &session{
		paths:       paths,
		values:      values,
		pathToIndex: pathToIndex,
	}
	e.mu.Unlock()

	logger.Info("SCE.Commit completed",
		logger.String("root", comm.String()),
		logger.Int("arc_count", len(paths)),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return comm, nil
}

// Prove generates a proof for an arc.
func (e *Engine) Prove(root cid.Cid, arcs arcset.View, path string) (cid.Cid, []byte, error) {
	start := time.Now()

	logger.Debug("SCE.Prove started",
		logger.String("root", root.String()),
		logger.String("path", path))

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		logger.Error("SCE.Prove extract commitment failed",
			logger.String("root", root.String()),
			logger.Err(err))
		return cid.Cid{}, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	e.mu.RLock()
	sess, ok := e.sessions[string(commBytes)]
	e.mu.RUnlock()

	if !ok {
		logger.Warn("SCE.Prove session not found",
			logger.String("root", root.String()))
		return cid.Cid{}, nil, ErrSessionNotFound
	}

	_, ok = arcs.Get(path)
	if !ok {
		logger.Error("SCE.Prove path not found in arc set",
			logger.String("root", root.String()),
			logger.String("path", path))
		return cid.Cid{}, nil, ErrPathNotFound
	}

	_, ok = sess.pathToIndex[path]
	if !ok {
		logger.Error("SCE.Prove path not found in session",
			logger.String("root", root.String()),
			logger.String("path", path))
		return cid.Cid{}, nil, ErrPathNotFound
	}

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
func (e *Engine) Update(root cid.Cid, arcs arcset.View, path string, oldKey, newKey cid.Cid) (cid.Cid, error) {
	start := time.Now()

	logger.Debug("SCE.Update started",
		logger.String("root", root.String()),
		logger.String("path", path),
		logger.String("old_key", oldKey.String()),
		logger.String("new_key", newKey.String()))

	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		logger.Error("SCE.Update extract commitment failed",
			logger.String("root", root.String()),
			logger.Err(err))
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	sess, ok := e.sessions[string(commBytes)]
	if !ok {
		logger.Warn("SCE.Update session not found",
			logger.String("root", root.String()))
		return cid.Cid{}, ErrSessionNotFound
	}

	_, ok = sess.pathToIndex[path]
	if !ok {
		logger.Error("SCE.Update path not found in session",
			logger.String("root", root.String()),
			logger.String("path", path))
		return cid.Cid{}, ErrPathNotFound
	}

	newComm, err := e.scheme.Update(root, arcs, path, oldKey, newKey)
	if err != nil {
		logger.Error("SCE.Update scheme failed",
			logger.String("root", root.String()),
			logger.String("path", path),
			logger.Err(err))
		return cid.Cid{}, err
	}

	// Extract new commitment bytes for session key
	newCommBytes, err := codec.ExtractCommitment(newComm)
	if err != nil {
		logger.Error("SCE.Update extract new commitment failed",
			logger.Err(err))
		return cid.Cid{}, fmt.Errorf("failed to extract new commitment: %w", err)
	}

	// Update session
	index := sess.pathToIndex[path]
	sess.values[index] = newKey
	e.sessions[string(newCommBytes)] = sess

	logger.Info("SCE.Update completed",
		logger.String("old_root", root.String()),
		logger.String("new_root", newComm.String()),
		logger.String("path", path),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return newComm, nil
}

// BatchUpdate updates multiple arcs.
func (e *Engine) BatchUpdate(root cid.Cid, arcs arcset.View, updates map[string]struct {
	Old cid.Cid
	New cid.Cid
}) (cid.Cid, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	sess, ok := e.sessions[string(commBytes)]
	if !ok {
		return cid.Cid{}, ErrSessionNotFound
	}

	// Validate all paths exist
	for path := range updates {
		if _, ok := sess.pathToIndex[path]; !ok {
			return cid.Cid{}, ErrPathNotFound
		}
	}

	newComm, err := e.scheme.BatchUpdate(root, arcs, updates)
	if err != nil {
		return cid.Cid{}, err
	}

	// Extract new commitment bytes for session key
	newCommBytes, err := codec.ExtractCommitment(newComm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract new commitment: %w", err)
	}

	// Update session values
	for path, update := range updates {
		index := sess.pathToIndex[path]
		sess.values[index] = update.New
	}
	e.sessions[string(newCommBytes)] = sess

	return newComm, nil
}

// BatchProve generates proofs for multiple paths.
func (e *Engine) BatchProve(root cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}

	e.mu.RLock()
	sess, ok := e.sessions[string(commBytes)]
	e.mu.RUnlock()

	if !ok {
		return nil, ErrSessionNotFound
	}

	// Validate all paths exist
	for _, path := range paths {
		if _, ok := sess.pathToIndex[path]; !ok {
			return nil, ErrPathNotFound
		}
	}

	return e.scheme.BatchProve(root, arcs, paths)
}

// BatchVerify verifies multiple proofs.
func (e *Engine) BatchVerify(root cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return e.scheme.BatchVerify(root, proofs)
}

// AggregateProve generates an aggregated proof.
func (e *Engine) AggregateProve(root cid.Cid, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("paths must not be empty")
	}

	e.mu.RLock()
	sess, ok := e.sessions[string(commBytes)]
	e.mu.RUnlock()

	if !ok {
		return nil, ErrSessionNotFound
	}

	// Validate all paths exist
	for _, path := range paths {
		if _, ok := sess.pathToIndex[path]; !ok {
			return nil, ErrPathNotFound
		}
	}

	return e.scheme.AggregateProve(root, arcs, paths)
}

// AggregateVerify verifies an aggregated proof.
func (e *Engine) AggregateVerify(root cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	return e.scheme.AggregateVerify(root, aggProof)
}

// extractArcs extracts sorted paths, values, and pathToIndex from ArcSetView.
func extractArcs(arcs arcset.View) ([]string, []cid.Cid, map[string]int, error) {
	var paths []string
	iter := arcs.Iterate()
	for {
		path, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
	}
	if iter.Err() != nil {
		return nil, nil, nil, iter.Err()
	}
	sort.Strings(paths)

	values := make([]cid.Cid, len(paths))
	pathToIndex := make(map[string]int, len(paths))

	for i, path := range paths {
		value, ok := arcs.Get(path)
		if !ok {
			return nil, nil, nil, fmt.Errorf("path %s disappeared during iteration", path)
		}
		values[i] = value
		pathToIndex[path] = i
	}

	return paths, values, pathToIndex, nil
}