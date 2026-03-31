// Package sce defines the Structure Commitment Engine interfaces.
// SCE is an internal component responsible for cryptographic commitments,
// proof generation, and verification.
package sce

import (
	"fmt"

	"github.com/dewebprotocol/malt/key"
)

// Proof represents a cryptographic proof that a specific arc
// is in a committed arc set.
type Proof []byte

// String returns a hex representation of the proof.
func (p Proof) String() string {
	if len(p) == 0 {
		return "<empty proof>"
	}
	return fmt.Sprintf("%x", []byte(p))
}

// IsEmpty checks if the proof is empty.
func (p Proof) IsEmpty() bool {
	return len(p) == 0
}

// Equals checks if two proofs are equal.
func (p Proof) Equals(other Proof) bool {
	if len(p) != len(other) {
		return false
	}
	for i := range p {
		if p[i] != other[i] {
			return false
		}
	}
	return true
}

// ArcIterator iterates over arcs in an arc set.
type ArcIterator interface {
	// Next advances to the next arc.
	// Returns (path, key, true) if there is an arc, or (_, _, false) if done.
	Next() (path string, k key.Key, ok bool)

	// Err returns any error encountered during iteration.
	Err() error
}

// ArcSetView provides a read-only view of an arc set.
// It is used by SCE to commit, prove, and update structures.
type ArcSetView interface {
	// Get retrieves the target key for a path.
	// Returns (key, true) if found, or (nil, false) if not found.
	Get(path string) (key.Key, bool)

	// Iterate returns an iterator over all arcs.
	Iterate() ArcIterator

	// Len returns the number of arcs.
	Len() int
}

// CommitmentScheme defines the interface for structure commitment backends.
// Implementations include IPA, KZG, Verkle, etc.
type CommitmentScheme interface {
	// Commit generates a commitment to an arc set.
	// Returns a StructureRoot (as Key) that can be used for resolution.
	Commit(arcs ArcSetView) (key.Key, error)

	// Prove generates a proof that a specific arc is in the committed set.
	// Returns the target key and a proof.
	Prove(root key.Key, arcs ArcSetView, path string) (key.Key, Proof, error)

	// Verify checks if a proof is valid for a given arc.
	Verify(root key.Key, path string, target key.Key, proof Proof) (bool, error)

	// Update updates the commitment for a changed arc.
	// Returns a new StructureRoot (as Key) after the update.
	Update(root key.Key, arcs ArcSetView, path string, oldKey, newKey key.Key) (key.Key, error)

	// BatchUpdate updates multiple arcs in a single operation.
	BatchUpdate(root key.Key, arcs ArcSetView, updates map[string]struct {
		Old key.Key
		New key.Key
	}) (key.Key, error)
}