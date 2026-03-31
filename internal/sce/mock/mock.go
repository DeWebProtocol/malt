// Package mock provides a mock implementation of sce.CommitmentScheme.
// It uses SHA256 hashing - NOT suitable for production.
package mock

import (
	"crypto/sha256"
	"fmt"
	"sort"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
)

// Commitment is a simple mock implementation for testing.
type Commitment struct {
	vectorSize int
}

// NewCommitment creates a new mock commitment scheme.
func NewCommitment(vectorSize int) *Commitment {
	if vectorSize <= 0 {
		vectorSize = 256
	}
	return &Commitment{vectorSize: vectorSize}
}

// Commit generates a commitment to an arc set.
func (m *Commitment) Commit(arcs sce.ArcSetView) (key.Key, error) {
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
func (m *Commitment) Prove(root key.Key, arcs sce.ArcSetView, path string) (key.Key, sce.Proof, error) {
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

	return target, sce.Proof(proof), nil
}

// Verify checks if a proof is valid.
func (m *Commitment) Verify(root key.Key, path string, target key.Key, proof sce.Proof) (bool, error) {
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
func (m *Commitment) Update(root key.Key, arcs sce.ArcSetView, path string, oldKey, newKey key.Key) (key.Key, error) {
	// Create a modified view with the new value
	modified := sce.NewMapArcSetView()

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
func (m *Commitment) BatchUpdate(root key.Key, arcs sce.ArcSetView, updates map[string]struct {
	Old key.Key
	New key.Key
}) (key.Key, error) {
	// For mock, just recompute the full commitment
	return m.Commit(arcs)
}

// getSortedPaths returns sorted paths from an ArcSetView.
func (m *Commitment) getSortedPaths(arcs sce.ArcSetView) []string {
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

// Ensure Commitment implements sce.CommitmentScheme.
var _ sce.CommitmentScheme = (*Commitment)(nil)