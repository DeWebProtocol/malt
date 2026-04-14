package graph

import (
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// Proof represents a verifiable proof for graph resolution.
type Proof interface {
	// Transcript returns the step-by-step resolution evidence.
	Transcript() *resolver.Transcript

	// Verify verifies the proof against a root and expected target.
	Verify(root cid.Cid, expectedTarget cid.Cid) (bool, error)

	// Size returns the proof size in bytes.
	Size() int
}

// AggregatedProof represents a batch proof for multiple paths.
// This remains a placeholder until aggregated graph-level verification is implemented.
type AggregatedProof struct {
	Commitment cid.Cid
	Results    map[string]cid.Cid
	ProofData  []byte
	Backend    string
}

// Verify verifies the aggregated proof against the commitment.
func (p *AggregatedProof) Verify() (bool, error) {
	return false, nil
}

// Size returns the proof size in bytes.
func (p *AggregatedProof) Size() int {
	return len(p.ProofData)
}

// UpdateDelta records the changes from an update operation.
type UpdateDelta struct {
	OldRoot cid.Cid
	NewRoot cid.Cid
	Added   []string
	Updated []string
	Deleted []string

	// RewriteAmplification is the number of nodes rewritten.
	// For MALT, this should always be 1 (localized update).
	RewriteAmplification float64
}

// SnapshotView is re-exported here to keep graph-facing method signatures local.
type SnapshotView = arcset.View
