// Package interfaces defines the shared interfaces used by the MALT codebase.
package interfaces

import (
	"context"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// Proof represents a cryptographic proof for resolution.
// This wraps the transcript and provides verification methods.
type Proof interface {
	// Transcript returns the step-by-step evidence.
	Transcript() *Transcript

	// Verify verifies the proof against a root and expected target.
	Verify(root cid.Cid, expectedTarget cid.Cid) (bool, error)

	// Size returns the proof size in bytes.
	Size() int
}

// Transcript records the evidence for each resolution step.
type Transcript struct {
	Steps []StepEvidence
}

// StepEvidence represents evidence for a single resolution step.
type StepEvidence struct {
	// Path is the path segment consumed in this step
	Path string

	// Target is the CID resolved to
	Target cid.Cid

	// Evidence is the cryptographic evidence for this step
	Evidence evidence.Evidence
}

// AggregatedProof represents a batch proof for multiple paths.
// This is returned by BatchResolve for efficient batch verification.
type AggregatedProof struct {
	// Commitment is the root CID being proved
	Commitment cid.Cid

	// Results maps path to resolved CID
	Results map[string]cid.Cid

	// ProofData is the aggregated proof bytes
	ProofData []byte

	// Backend indicates which commitment scheme generated this
	Backend string
}

// Verify verifies the aggregated proof against the commitment.
func (p *AggregatedProof) Verify() (bool, error) {
	// Aggregated verification is not currently implemented in this adapter layer.
	return false, nil
}

// Size returns the proof size in bytes.
func (p *AggregatedProof) Size() int {
	return len(p.ProofData)
}

// GraphResolver is the read-only interface for MALT graph resolution.
type GraphResolver interface {
	// Resolve resolves a path from a root CID, returning the target and proof.
	// Native explicit-arc resolution is the primary path. Ordinary CID traversal
	// is used when resolution crosses into interoperable legacy IPLD/Merkle space.
	// Returns the resolved CID and a proof that can be verified.
	Resolve(ctx context.Context, root cid.Cid, path string) (cid.Cid, Proof, error)

	// BatchResolve resolves multiple paths from a root CID.
	// Returns an aggregated proof for efficient batch verification.
	// This is more efficient than individual Resolve calls.
	BatchResolve(ctx context.Context, root cid.Cid, paths []string) (map[string]cid.Cid, *AggregatedProof, error)

	// Verify verifies a proof against a root and expected target.
	Verify(ctx context.Context, root cid.Cid, proof Proof, expectedTarget cid.Cid) (bool, error)

	// BatchVerify verifies an aggregated proof against a root.
	BatchVerify(ctx context.Context, root cid.Cid, aggProof *AggregatedProof) (bool, error)
}

// GraphWriter is the write-side interface for MALT graph mutations.
type GraphWriter interface {
	// Update updates arcs under a root, returning the new root and deltas.
	// Arcs to delete should have cid.Undef as target.
	// The new root is a new commitment to the updated arc set.
	Update(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *UpdateDelta, error)

	// BatchUpdate is a synonym for Update (Update already supports batch).
	// Kept for API consistency.
	BatchUpdate(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *UpdateDelta, error)

	// Snapshot returns an immutable view of the arc set under a root.
	// This is used to generate new commitments or for state export.
	Snapshot(ctx context.Context, root cid.Cid) (arcset.Snapshot, error)

	// Commit generates a new commitment from a snapshot.
	// Returns the new root CID.
	Commit(ctx context.Context, snapshot arcset.View) (cid.Cid, error)
}

// Graph is the full interface combining resolution and mutation.
type Graph interface {
	GraphResolver
	GraphWriter
}

// UpdateDelta records the changes from an update operation.
type UpdateDelta struct {
	// OldRoot is the previous root CID
	OldRoot cid.Cid

	// NewRoot is the new root CID after update
	NewRoot cid.Cid

	// Added is the set of paths that were added
	Added []string

	// Updated is the set of paths that were updated
	Updated []string

	// Deleted is the set of paths that were deleted
	Deleted []string

	// RewriteAmplification is the number of nodes rewritten
	// For MALT, this should always be 1 (localized update)
	RewriteAmplification float64
}
