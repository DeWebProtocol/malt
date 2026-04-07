// Package sce defines the Structure Commitment Engine.
// SCE coordinates arc set management and delegates to commitment schemes.
package sce

import (
	"fmt"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/sce/commitment"
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
	if arcs == nil {
		return cid.Cid{}, fmt.Errorf("arc set is nil")
	}

	// Delegate to commitment scheme
	comm, err := e.scheme.Commit(arcs)
	if err != nil {
		return cid.Cid{}, err
	}

	// Extract and store session data
	paths, values, pathToIndex, err := extractArcs(arcs)
	if err != nil {
		return cid.Cid{}, err
	}

	// Extract commitment bytes for session key
	commBytes, err := codec.ExtractCommitment(comm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	e.mu.Lock()
	e.sessions[string(commBytes)] = &session{
		paths:       paths,
		values:      values,
		pathToIndex: pathToIndex,
	}
	e.mu.Unlock()

	return comm, nil
}

// Prove generates a proof for an arc.
func (e *Engine) Prove(root cid.Cid, arcs arcset.View, path string) (cid.Cid, []byte, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	e.mu.RLock()
	sess, ok := e.sessions[string(commBytes)]
	e.mu.RUnlock()

	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("commitment session not found")
	}

	_, ok = arcs.Get(path)
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found in arc set", path)
	}

	_, ok = sess.pathToIndex[path]
	if !ok {
		return cid.Cid{}, nil, fmt.Errorf("path %s not found in session", path)
	}

	return e.scheme.Prove(root, arcs, path)
}

// Verify verifies a proof.
func (e *Engine) Verify(root cid.Cid, path string, target cid.Cid, proof []byte) (bool, error) {
	return e.scheme.Verify(root, path, target, proof)
}

// Update updates an arc.
func (e *Engine) Update(root cid.Cid, arcs arcset.View, path string, oldKey, newKey cid.Cid) (cid.Cid, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract commitment: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	sess, ok := e.sessions[string(commBytes)]
	if !ok {
		return cid.Cid{}, fmt.Errorf("commitment session not found")
	}

	_, ok = sess.pathToIndex[path]
	if !ok {
		return cid.Cid{}, fmt.Errorf("path %s not found in session", path)
	}

	newComm, err := e.scheme.Update(root, arcs, path, oldKey, newKey)
	if err != nil {
		return cid.Cid{}, err
	}

	// Extract new commitment bytes for session key
	newCommBytes, err := codec.ExtractCommitment(newComm)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to extract new commitment: %w", err)
	}

	// Update session
	index := sess.pathToIndex[path]
	sess.values[index] = newKey
	e.sessions[string(newCommBytes)] = sess

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
		return cid.Cid{}, fmt.Errorf("commitment session not found")
	}

	// Validate all paths exist
	for path := range updates {
		if _, ok := sess.pathToIndex[path]; !ok {
			return cid.Cid{}, fmt.Errorf("path %s not found in session", path)
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

// ProveBatch generates proofs for multiple paths.
func (e *Engine) ProveBatch(root cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("paths cannot be empty")
	}

	e.mu.RLock()
	sess, ok := e.sessions[string(commBytes)]
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment session not found")
	}

	// Validate all paths exist
	for _, path := range paths {
		if _, ok := sess.pathToIndex[path]; !ok {
			return nil, fmt.Errorf("path %s not found in session", path)
		}
	}

	return e.scheme.ProveBatch(root, arcs, paths)
}

// VerifyBatch verifies multiple proofs.
func (e *Engine) VerifyBatch(root cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return e.scheme.VerifyBatch(root, proofs)
}

// ProveAggregate generates an aggregated proof.
func (e *Engine) ProveAggregate(root cid.Cid, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error) {
	// Extract commitment bytes from MALT CID
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return nil, fmt.Errorf("failed to extract commitment: %w", err)
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("paths cannot be empty")
	}

	e.mu.RLock()
	sess, ok := e.sessions[string(commBytes)]
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("commitment session not found")
	}

	// Validate all paths exist
	for _, path := range paths {
		if _, ok := sess.pathToIndex[path]; !ok {
			return nil, fmt.Errorf("path %s not found in session", path)
		}
	}

	return e.scheme.ProveAggregate(root, arcs, paths)
}

// VerifyAggregate verifies an aggregated proof.
func (e *Engine) VerifyAggregate(root cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	return e.scheme.VerifyAggregate(root, aggProof)
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