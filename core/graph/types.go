package graph

import (
	"github.com/dewebprotocol/malt/core/resolver"
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
