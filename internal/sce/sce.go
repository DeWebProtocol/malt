// Package sce defines the Structure Commitment Engine interfaces.
// SCE is an internal component responsible for cryptographic commitments,
// proof generation, and verification.
package sce

import (
	"fmt"
	"sort"

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

	// === Aggregated Proof Methods ===

	// ProveBatch generates proofs for multiple paths.
	// Returns a map from path to (target, proof) pairs.
	ProveBatch(root key.Key, arcs ArcSetView, paths []string) (map[string]BatchProofEntry, error)

	// VerifyBatch verifies multiple proofs efficiently.
	// Implementations may use aggregation for better performance.
	VerifyBatch(root key.Key, proofs map[string]BatchProofEntry) (bool, error)

	// ProveAggregate generates a single aggregated proof for multiple paths.
	// The aggregated proof is smaller than individual proofs combined.
	ProveAggregate(root key.Key, arcs ArcSetView, paths []string) (*AggregatedProof, error)

	// VerifyAggregate verifies an aggregated proof for multiple paths.
	VerifyAggregate(root key.Key, aggProof *AggregatedProof) (bool, error)
}

// BatchProofEntry represents a single proof in a batch.
type BatchProofEntry struct {
	Target key.Key
	Proof  Proof
}

// AggregatedProof represents a proof for multiple arcs.
// The internal structure depends on the commitment scheme:
// - KZG: Multiple evaluation proofs in one
// - IPA: Aggregated inner product argument
// - Verkle: IPA-based aggregated proof
type AggregatedProof struct {
	// Paths are the paths being proved
	Paths []string

	// Targets are the target keys for each path
	Targets []key.Key

	// ProofData is the scheme-specific aggregated proof
	ProofData []byte
}

// MapArcSetView is a simple in-memory ArcSetView implementation.
// Useful for constructing arc sets before commitment.
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