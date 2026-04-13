// Package commitment provides CommitmentBackend implementations.
package commitment

import (
	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// SchemeBackend wraps a commitment.Scheme to implement CommitmentBackend.
type SchemeBackend struct {
	scheme commitment.Scheme
	name   string
}

// NewSchemeBackend creates a new CommitmentBackend wrapping a scheme.
func NewSchemeBackend(scheme commitment.Scheme, name string) *SchemeBackend {
	return &SchemeBackend{
		scheme: scheme,
		name:   name,
	}
}

// Commit generates a commitment to an arc set.
func (b *SchemeBackend) Commit(arcs arcset.View) (cid.Cid, error) {
	return b.scheme.Commit(arcs)
}

// Prove generates a proof for a single path.
func (b *SchemeBackend) Prove(commitment cid.Cid, arcs arcset.View, path string) (cid.Cid, []byte, error) {
	return b.scheme.Prove(commitment, arcs, path)
}

// Verify verifies a proof for a single path.
func (b *SchemeBackend) Verify(commitment cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	return b.scheme.Verify(commitment, path, value, proof)
}

// BatchProve generates proofs for multiple paths.
func (b *SchemeBackend) BatchProve(commitment cid.Cid, arcs arcset.View, paths []string) (map[string]arcset.BatchProofEntry, error) {
	return b.scheme.BatchProve(commitment, arcs, paths)
}

// BatchVerify verifies multiple proofs.
func (b *SchemeBackend) BatchVerify(commitment cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return b.scheme.BatchVerify(commitment, proofs)
}

// AggregateProve generates an aggregated proof for multiple paths.
func (b *SchemeBackend) AggregateProve(commitment cid.Cid, arcs arcset.View, paths []string) (*arcset.AggregatedProof, error) {
	return b.scheme.AggregateProve(commitment, arcs, paths)
}

// AggregateVerify verifies an aggregated proof.
func (b *SchemeBackend) AggregateVerify(commitment cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	return b.scheme.AggregateVerify(commitment, aggProof)
}

// Update updates a single value in the commitment.
func (b *SchemeBackend) Update(commitment cid.Cid, arcs arcset.View, path string, oldValue, newValue cid.Cid) (cid.Cid, error) {
	return b.scheme.Update(commitment, arcs, path, oldValue, newValue)
}

// BatchUpdate updates multiple values in the commitment.
func (b *SchemeBackend) BatchUpdate(commitment cid.Cid, arcs arcset.View, updates map[string]struct {
	Old cid.Cid
	New cid.Cid
}) (cid.Cid, error) {
	return b.scheme.BatchUpdate(commitment, arcs, updates)
}

// Name returns the backend name.
func (b *SchemeBackend) Name() string {
	return b.name
}

// Ensure SchemeBackend implements CommitmentBackend.
var _ interfaces.CommitmentBackend = (*SchemeBackend)(nil)