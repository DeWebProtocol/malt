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

	// CommitValues generates a commitment to a stable indexed cell vector.
	CommitValues(values []Cell) (cid.Cid, error)

	// ProveIndex generates a proof for one index and returns the proved cell.
	ProveIndex(root cid.Cid, values []Cell, index uint64) (Cell, []byte, error)

	// VerifyIndex verifies a proof for one index against the expected cell.
	VerifyIndex(root cid.Cid, index uint64, value Cell, proof []byte) (bool, error)

	// VerifyProof verifies a proof that already carries its own index metadata.
	VerifyProof(root cid.Cid, value Cell, proof []byte) (bool, error)

	// ReplaceIndex performs an index-stable replacement and returns the new root.
	ReplaceIndex(root cid.Cid, values []Cell, index uint64, oldValue, newValue Cell) (cid.Cid, error)
}
