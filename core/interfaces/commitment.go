// Package interfaces defines the shared interfaces used by the MALT codebase.
package interfaces

import (
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// CommitmentBackend is an optional compatibility interface for cryptographic
// commitment operations. The canonical MALT path uses commitment schemes
// through SCE rather than through this adapter.
type CommitmentBackend interface {
	// Commit generates a commitment to an arc set.
	// Returns a CID with MALT-specific codec (e.g., malt-kzg, malt-verkle).
	Commit(arcs arcset.View) (cid.Cid, error)

	// Prove generates a proof for a single path.
	// Returns the target CID and proof bytes.
	Prove(commitment cid.Cid, arcs arcset.View, path string) (cid.Cid, []byte, error)

	// Verify verifies a proof for a single path.
	Verify(commitment cid.Cid, path string, value cid.Cid, proof []byte) (bool, error)

	// BatchProve generates proofs for multiple paths.
	BatchProve(commitment cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error)

	// BatchVerify verifies multiple proofs.
	BatchVerify(commitment cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error)

	// AggregateProve generates an aggregated proof for multiple paths.
	// This is more efficient than individual proofs for batch verification.
	AggregateProve(commitment cid.Cid, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error)

	// AggregateVerify verifies an aggregated proof.
	AggregateVerify(commitment cid.Cid, aggProof *arcset.AggregatedProof) (bool, error)

	// Update updates a single value in the commitment.
	// Returns the new commitment CID.
	Update(commitment cid.Cid, arcs arcset.View, path string, oldValue, newValue cid.Cid) (cid.Cid, error)

	// BatchUpdate updates multiple values in the commitment.
	// Returns the new commitment CID.
	BatchUpdate(commitment cid.Cid, arcs arcset.View, updates map[string]struct {
		Old cid.Cid
		New cid.Cid
	}) (cid.Cid, error)

	// Name returns the backend name (e.g., "kzg", "verkle", "ipa", "merkle").
	Name() string
}
