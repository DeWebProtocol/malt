// Package commitment defines cryptographic commitment interfaces.
// Primitive backends in this package are restart-safe and do not rely on
// RAM-only state for correctness.
package commitment

import (
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// Scheme is the path-oriented commitment interface still consumed by the
// current graph/writer/resolver stack. Higher semantic layers may adapt this
// interface to their own local runtime needs.
type Scheme interface {
	// Commit generates a commitment to an arc set.
	// Returns a CID with a MALT-specific codec (e.g., malt-kzg, malt-ipa).
	Commit(arcs arcset.ArcSet) (cid.Cid, error)

	// Prove generates a proof for a single path.
	// Returns the target CID and proof bytes.
	Prove(commitment cid.Cid, arcs arcset.ArcSet, path string) (cid.Cid, []byte, error)

	// Verify verifies a proof.
	Verify(commitment cid.Cid, path string, value cid.Cid, proof []byte) (bool, error)

	// Update updates a single value in the commitment.
	// Returns the new commitment CID.
	Update(commitment cid.Cid, arcs arcset.ArcSet, path string, oldValue, newValue cid.Cid) (cid.Cid, error)

	// BatchUpdate updates multiple values.
	// Returns the new commitment CID.
	BatchUpdate(commitment cid.Cid, arcs arcset.ArcSet, updates map[string]struct {
		Old cid.Cid
		New cid.Cid
	}) (cid.Cid, error)

	// BatchProve generates proofs for multiple paths.
	BatchProve(commitment cid.Cid, arcs arcset.ArcSet, paths []string) (map[string]arcset.BatchProofEntry, error)

	// BatchVerify verifies multiple proofs.
	BatchVerify(commitment cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error)

	// AggregateProve generates an aggregated proof.
	AggregateProve(commitment cid.Cid, arcs arcset.ArcSet, paths []string) (*arcset.AggregatedProof, error)

	// AggregateVerify verifies an aggregated proof.
	AggregateVerify(commitment cid.Cid, aggProof *arcset.AggregatedProof) (bool, error)
}
