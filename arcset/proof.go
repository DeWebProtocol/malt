// Package arcset defines interfaces and types for arc sets.
package arcset

import "github.com/dewebprotocol/malt/key"

// Proof represents a cryptographic proof.
type Proof []byte

// BatchProofEntry represents a single proof in a batch.
type BatchProofEntry struct {
	Target key.Key
	Proof  Proof
}

// AggregatedProof represents a proof for multiple values.
type AggregatedProof struct {
	Paths     []string
	Targets   []key.Key
	ProofData []byte
}