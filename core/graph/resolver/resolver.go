// Package resolver provides resolution logic for Graph.
package resolver

import (
	"context"

	"github.com/dewebprotocol/malt/core/interfaces"
	cid "github.com/ipfs/go-cid"
)

// ResolveResult contains the result of a resolution operation.
type ResolveResult struct {
	// Target is the final resolved CID
	Target cid.Cid

	// Transcript contains the evidence for each step
	Transcript *interfaces.Transcript
}

// Resolver resolves paths from roots using explicit and implicit steps.
type Resolver interface {
	// Resolve resolves a path from a root CID.
	Resolve(ctx context.Context, root cid.Cid, path string) (*ResolveResult, error)

	// VerifyTranscript verifies all steps in a transcript.
	VerifyTranscript(ctx context.Context, root cid.Cid, transcript *interfaces.Transcript) (bool, error)
}
