// Package commitment defines the pure cryptographic commitment interface.
// Implementations include KZG, Verkle, and IPA.
package commitment

import (
	"github.com/dewebprotocol/malt/types/arcset"
	"github.com/dewebprotocol/malt/key"
)

// Scheme defines the pure commitment interface.
// Implementations are KZG, Verkle, IPA, etc.
type Scheme interface {
	// Commit generates a commitment to an arc set.
	// Returns a commitment (as Key) that can be used for proving.
	Commit(arcs arcset.View) (key.Key, error)

	// Prove generates a proof for a single path.
	Prove(commitment key.Key, arcs arcset.View, path string) (key.Key, arcset.Proof, error)

	// Verify verifies a proof.
	Verify(commitment key.Key, path string, value key.Key, proof arcset.Proof) (bool, error)

	// Update updates a single value in the commitment.
	Update(commitment key.Key, arcs arcset.View, path string, oldValue, newValue key.Key) (key.Key, error)

	// BatchUpdate updates multiple values.
	BatchUpdate(commitment key.Key, arcs arcset.View, updates map[string]struct {
		Old key.Key
		New key.Key
	}) (key.Key, error)

	// ProveBatch generates proofs for multiple paths.
	ProveBatch(commitment key.Key, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error)

	// VerifyBatch verifies multiple proofs.
	VerifyBatch(commitment key.Key, proofs map[string]arcset.BatchProofEntry) (bool, error)

	// ProveAggregate generates an aggregated proof.
	ProveAggregate(commitment key.Key, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error)

	// VerifyAggregate verifies an aggregated proof.
	VerifyAggregate(commitment key.Key, aggProof *arcset.AggregatedProof) (bool, error)
}