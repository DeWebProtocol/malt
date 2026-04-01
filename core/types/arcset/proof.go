// Package arcset defines interfaces and types for arc sets.
package arcset

import cid "github.com/ipfs/go-cid"

// Proof represents a cryptographic proof.
type Proof []byte

// BatchProofEntry represents a single proof in a batch.
type BatchProofEntry struct {
	Target cid.Cid
	Proof  Proof
}

// AggregatedProof represents a proof for multiple values.
type AggregatedProof struct {
	Paths     []string
	Targets   []cid.Cid
	ProofData []byte
}