// Package commitment defines cryptographic commitment interfaces.
// Primitive backends in this package are restart-safe and do not rely on
// RAM-only state for correctness.
package commitment

import (
	cid "github.com/ipfs/go-cid"
)

// IndexCommitment is the semantic-neutral primitive interface for
// index-addressed authenticated values.
type IndexCommitment interface {
	// MaxValues returns the maximum number of authenticated values per root.
	MaxValues() int

	// Commit generates a commitment to a stable indexed cell vector.
	Commit(values []Cell) (cid.Cid, error)

	// Prove generates a proof for one index and returns the proved cell.
	Prove(values []Cell, index uint64) (root cid.Cid, value Cell, proof []byte, err error)

	// BatchProve generates one proof payload for an ordered index list and
	// returns the proved cells in the same order as indices.
	BatchProve(values []Cell, indices []uint64) (root cid.Cid, proved []Cell, proof []byte, err error)

	// VerifyIndex verifies a proof for one index against the expected cell.
	VerifyIndex(root cid.Cid, index uint64, value Cell, proof []byte) (bool, error)

	// BatchVerify verifies a proof payload for an ordered index list against
	// the expected cells in the same order as indices.
	BatchVerify(root cid.Cid, indices []uint64, values []Cell, proof []byte) (bool, error)

	// VerifyProof verifies a proof that already carries its own index metadata.
	VerifyProof(root cid.Cid, value Cell, proof []byte) (bool, error)

	// Replace performs an index-stable replacement and returns the new root.
	Replace(values []Cell, index uint64, oldValue, newValue Cell) (cid.Cid, error)
}
