// Package arcset defines interfaces and types for arc sets.
package arcset

import cid "github.com/ipfs/go-cid"

// BatchProofEntry represents a single proof in a batch.
type BatchProofEntry struct {
	Target cid.Cid
	Proof  []byte
}

// AggregatedProof represents a proof for multiple values.
type AggregatedProof struct {
	Paths     []string
	Targets   []cid.Cid
	ProofData []byte
}