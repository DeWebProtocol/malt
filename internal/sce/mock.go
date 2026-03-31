// Package sce defines the Structure Commitment Engine interfaces.
package sce

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/dewebprotocol/malt/key"
)

// MockCommitment is a simple mock implementation for testing.
// It uses SHA256 hashing - NOT suitable for production.
type MockCommitment struct {
	vectorSize int
}

// NewMockCommitment creates a new mock commitment scheme.
func NewMockCommitment(vectorSize int) *MockCommitment {
	if vectorSize <= 0 {
		vectorSize = 256
	}
	return &MockCommitment{vectorSize: vectorSize}
}

// Commit generates a commitment to an arc set.
func (m *MockCommitment) Commit(arcs ArcSetView) (key.Key, error) {
	// Hash all arcs together
	h := sha256.New()

	// Get sorted paths for deterministic ordering
	paths := m.getSortedPaths(arcs)

	for _, p := range paths {
		k, ok := arcs.Get(p)
		if !ok {
			continue
		}
		h.Write([]byte(p))
		h.Write(k.Bytes())
	}

	commitment := h.Sum(nil)
	return key.NewStructureRoot(commitment), nil
}

// Prove generates a proof for an arc.
func (m *MockCommitment) Prove(root key.Key, arcs ArcSetView, path string) (key.Key, Proof, error) {
	target, ok := arcs.Get(path)
	if !ok {
		return nil, nil, fmt.Errorf("path not found: %s", path)
	}

	// Mock proof: hash of (root, path, target)
	h := sha256.New()
	h.Write(root.Bytes())
	h.Write([]byte(path))
	h.Write(target.Bytes())
	proof := h.Sum(nil)

	return target, Proof(proof), nil
}

// Verify checks if a proof is valid.
func (m *MockCommitment) Verify(root key.Key, path string, target key.Key, proof Proof) (bool, error) {
	// Recompute the mock proof
	h := sha256.New()
	h.Write(root.Bytes())
	h.Write([]byte(path))
	h.Write(target.Bytes())
	expected := h.Sum(nil)

	if len(proof) != len(expected) {
		return false, nil
	}

	for i := range proof {
		if proof[i] != expected[i] {
			return false, nil
		}
	}

	return true, nil
}

// Update updates the commitment for a changed arc.
func (m *MockCommitment) Update(root key.Key, arcs ArcSetView, path string, oldKey, newKey key.Key) (key.Key, error) {
	// Create a modified view with the new value
	modified := NewMapArcSetView()

	// Copy existing arcs
	iter := arcs.Iterate()
	for {
		p, k, ok := iter.Next()
		if !ok {
			break
		}
		modified.Add(p, k)
	}

	// Update the specific path
	modified.Add(path, newKey)

	// Recommit
	return m.Commit(modified)
}

// BatchUpdate updates multiple arcs.
func (m *MockCommitment) BatchUpdate(root key.Key, arcs ArcSetView, updates map[string]struct {
	Old key.Key
	New key.Key
}) (key.Key, error) {
	// For mock, just recompute the full commitment
	return m.Commit(arcs)
}

// getSortedPaths returns sorted paths from an ArcSetView.
func (m *MockCommitment) getSortedPaths(arcs ArcSetView) []string {
	paths := make([]string, 0, arcs.Len())
	iter := arcs.Iterate()
	for {
		p, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// MapArcSetView is a simple in-memory ArcSetView implementation.
type MapArcSetView struct {
	arcs map[string]key.Key
}

// NewMapArcSetView creates a new MapArcSetView.
func NewMapArcSetView() *MapArcSetView {
	return &MapArcSetView{
		arcs: make(map[string]key.Key),
	}
}

// Add adds an arc to the view.
func (v *MapArcSetView) Add(path string, k key.Key) {
	v.arcs[path] = k
}

// Get retrieves the target key for a path.
func (v *MapArcSetView) Get(path string) (key.Key, bool) {
	k, ok := v.arcs[path]
	return k, ok
}

// Iterate returns an iterator.
func (v *MapArcSetView) Iterate() ArcIterator {
	// Get sorted paths
	paths := make([]string, 0, len(v.arcs))
	for p := range v.arcs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	return &mapArcIterator{view: v, paths: paths, idx: -1}
}

// Len returns the number of arcs.
func (v *MapArcSetView) Len() int {
	return len(v.arcs)
}

// mapArcIterator implements ArcIterator.
type mapArcIterator struct {
	view  *MapArcSetView
	paths []string
	idx   int
	err   error
}

// Next advances to the next arc.
func (it *mapArcIterator) Next() (string, key.Key, bool) {
	it.idx++
	if it.idx >= len(it.paths) {
		return "", nil, false
	}
	path := it.paths[it.idx]
	k, _ := it.view.Get(path)
	return path, k, true
}

// Err returns any error.
func (it *mapArcIterator) Err() error {
	return it.err
}