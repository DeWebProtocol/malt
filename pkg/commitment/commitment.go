// Package commitment defines the structure commitment interface for MALT.
// The commitment scheme is the core of MALT's verifiable structure layer.
//
// The interface is designed to support:
// 1. Committing to an arc set with cryptographic binding
// 2. Proving individual arc membership without revealing the full set
// 3. Verifying proofs efficiently
// 4. Updating commitments locally without recomputing the entire structure
package commitment

import (
	"fmt"

	"github.com/dewebprotocol/malt/pkg/types"
)

// Commitment represents a cryptographic commitment to an arc set.
// The commitment binds all arcs in the set such that:
// - It's computationally infeasible to find two different arc sets with the same commitment
// - Individual arcs can be proven to be in the committed set
type Commitment []byte

// String returns a hex representation of the commitment.
func (c Commitment) String() string {
	if len(c) == 0 {
		return "<empty>"
	}
	return fmt.Sprintf("%x", []byte(c))
}

// Equals checks if two commitments are equal.
func (c Commitment) Equals(other Commitment) bool {
	if len(c) != len(other) {
		return false
	}
	for i := range c {
		if c[i] != other[i] {
			return false
		}
	}
	return true
}

// IsEmpty checks if the commitment is empty.
func (c Commitment) IsEmpty() bool {
	return len(c) == 0
}

// Proof represents a cryptographic proof that a specific arc is in a committed set.
// The proof should be verifiable given only the commitment, path, and claimed target.
type Proof []byte

// String returns a hex representation of the proof.
func (p Proof) String() string {
	if len(p) == 0 {
		return "<empty>"
	}
	return fmt.Sprintf("%x", []byte(p))
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

// IsEmpty checks if the proof is empty.
func (p Proof) IsEmpty() bool {
	return len(p) == 0
}

// CommitmentScheme defines the interface for structure commitment schemes.
// This interface abstracts the cryptographic details, allowing MALT to work
// with different commitment schemes (IPA, KZG, Verkle, etc.).
//
// Key properties a CommitmentScheme must satisfy:
// 1. Binding: Hard to find two different arc sets with the same commitment
// 2. Hiding: The commitment reveals nothing about arcs not explicitly proven
// 3. Local Updates: Update should be O(1), not requiring the full arc set
// 4. Succinct Proofs: Proofs should be sublinear in the size of the arc set
type CommitmentScheme interface {
	// Commit generates a commitment for an arc set.
	// The commitment cryptographically binds all arcs in the set.
	//
	// Parameters:
	//   - arcs: The arc set to commit to
	//
	// Returns:
	//   - The commitment to the arc set
	//   - An error if commitment fails
	Commit(arcs *types.ArcSet) (Commitment, error)

	// Prove generates a proof that a specific arc is in the committed set.
	// This allows proving arc membership without revealing the entire set.
	//
	// Parameters:
	//   - comm: The commitment to the arc set
	//   - arcs: The arc set (needed for proof generation)
	//   - p: The path of the arc to prove
	//
	// Returns:
	//   - The target CID for the path (or error if path not found)
	//   - A proof that (p, target) is in the committed set
	//   - An error if proof generation fails
	Prove(comm Commitment, arcs *types.ArcSet, p types.Path) (types.CID, Proof, error)

	// Verify checks if a proof is valid for a given arc.
	// This can be verified with only the commitment, path, target, and proof.
	//
	// Parameters:
	//   - comm: The commitment to the arc set
	//   - p: The path of the claimed arc
	//   - c: The claimed target CID
	//   - proof: The proof to verify
	//
	// Returns:
	//   - true if the proof is valid, false otherwise
	//   - An error if verification fails unexpectedly
	Verify(comm Commitment, p types.Path, c types.CID, proof Proof) (bool, error)

	// Update updates the commitment for a changed arc.
	// This is the key operation that enables local structural updates.
	//
	// For a commitment scheme that supports local updates (like IPA),
	// this should be O(1) and not require the full arc set.
	//
	// Parameters:
	//   - comm: The current commitment
	//   - p: The path of the arc to update
	//   - oldCID: The current target CID (must match for update to succeed)
	//   - newCID: The new target CID
	//
	// Returns:
	//   - The new commitment after the update
	//   - An error if the update fails
	Update(comm Commitment, p types.Path, oldCID, newCID types.CID) (Commitment, error)

	// BatchUpdate updates multiple arcs in a single operation.
	// This can be more efficient than individual updates for some schemes.
	//
	// Parameters:
	//   - comm: The current commitment
	//   - updates: Map of paths to (old CID, new CID) pairs
	//
	// Returns:
	//   - The new commitment after all updates
	//   - An error if any update fails
	BatchUpdate(comm Commitment, updates map[types.Path]struct {
		Old types.CID
		New types.CID
	}) (Commitment, error)
}

// ProveResult contains the result of a Prove operation.
type ProveResult struct {
	Target types.CID
	Proof  Proof
}

// VerifyResult contains the result of a Verify operation.
type VerifyResult struct {
	Valid bool
	Error error
}

// SchemeType identifies the type of commitment scheme.
type SchemeType string

const (
	SchemeTypeIPA    SchemeType = "ipa"
	SchemeTypeKZG    SchemeType = "kzg"
	SchemeTypeVerkle SchemeType = "verkle"
	SchemeTypeMock   SchemeType = "mock"
)

// Config contains configuration for a commitment scheme.
type Config struct {
	// Type specifies the commitment scheme type
	Type SchemeType

	// VectorSize is the maximum number of arcs supported
	VectorSize int

	// SecurityLevel is the security parameter in bits
	SecurityLevel int

	// TrustedSetup indicates if a trusted setup is required
	TrustedSetup bool

	// SRSPath is the path to the structured reference string (for KZG)
	SRSPath string
}