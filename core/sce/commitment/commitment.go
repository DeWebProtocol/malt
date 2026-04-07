// Package commitment defines the pure cryptographic commitment interface.
// Implementations include KZG, Verkle, and IPA.
package commitment

import (
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// Scheme defines the pure commitment interface.
// Implementations are KZG, Verkle, IPA, etc.
type Scheme interface {
	// Commit generates a commitment to an arc set.
	// Returns a CID with MALT-specific codec (e.g., malt-kzg, malt-verkle, malt-ipa).
	Commit(arcs arcset.View) (cid.Cid, error)

	// Prove generates a proof for a single path.
	// Returns the target CID and proof bytes.
	Prove(commitment cid.Cid, arcs arcset.View, path string) (cid.Cid, []byte, error)

	// Verify verifies a proof.
	Verify(commitment cid.Cid, path string, value cid.Cid, proof []byte) (bool, error)

	// Update updates a single value in the commitment.
	// Returns the new commitment CID.
	Update(commitment cid.Cid, arcs arcset.View, path string, oldValue, newValue cid.Cid) (cid.Cid, error)

	// BatchUpdate updates multiple values.
	// Returns the new commitment CID.
	BatchUpdate(commitment cid.Cid, arcs arcset.View, updates map[string]struct {
		Old cid.Cid
		New cid.Cid
	}) (cid.Cid, error)

	// ProveBatch generates proofs for multiple paths.
	ProveBatch(commitment cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error)

	// VerifyBatch verifies multiple proofs.
	VerifyBatch(commitment cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error)

	// ProveAggregate generates an aggregated proof.
	ProveAggregate(commitment cid.Cid, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error)

	// VerifyAggregate verifies an aggregated proof.
	VerifyAggregate(commitment cid.Cid, aggProof *arcset.AggregatedProof) (bool, error)
}