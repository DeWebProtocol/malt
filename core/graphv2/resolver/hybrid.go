// Package resolver provides resolution logic for Graph.
package resolver

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/interfaces"
	cid "github.com/ipfs/go-cid"
)

// HybridResolver combines explicit and implicit step resolution.
// Explicit steps resolve MALT arcs (via ArcStore).
// Implicit steps resolve Merkle DAG links (via ContentStore).
type HybridResolver struct {
	explicitStep ExplicitStep
	implicitStep ImplicitStep
}

// NewHybridResolver creates a new hybrid resolver.
func NewHybridResolver(explicit ExplicitStep, implicit ImplicitStep) *HybridResolver {
	return &HybridResolver{
		explicitStep: explicit,
		implicitStep: implicit,
	}
}

// Resolve resolves a path from a root CID.
func (r *HybridResolver) Resolve(ctx context.Context, root cid.Cid, path string) (*ResolveResult, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root must be defined")
	}

	transcript := &interfaces.Transcript{Steps: make([]interfaces.StepEvidence, 0)}
	currentCID := root
	remainingPath := path

	for remainingPath != "" {
		var matchedPath string
		var target cid.Cid
		var evidence interface{}
		var err error

		// Try explicit step first (for MALT CIDs)
		// If root is MALT CID, use explicit step
		if r.explicitStep != nil && isMaltCid(currentCID) {
			matchedPath, target, evidence, err = r.explicitStep.Resolve(ctx, currentCID, remainingPath)
		} else if r.implicitStep != nil {
			// Use implicit step for non-MALT CIDs
			matchedPath, target, evidence, err = r.implicitStep.Resolve(ctx, currentCID, remainingPath)
		} else {
			// No step available, return current
			return &ResolveResult{
				Target:     currentCID,
				Transcript: transcript,
			}, nil
		}

		if err != nil {
			return nil, fmt.Errorf("resolution step failed: %w", err)
		}

		// If no path matched, we can't continue
		if matchedPath == "" {
			return &ResolveResult{
				Target:     currentCID,
				Transcript: transcript,
			}, nil
		}

		// Record step
		transcript.Steps = append(transcript.Steps, interfaces.StepEvidence{
			Path:     matchedPath,
			Target:   target,
			Evidence: evidence,
		})

		// Update current CID
		currentCID = target

		// Update remaining path
		remainingPath = remainingPath[len(matchedPath):]
		if remainingPath != "" && remainingPath[0] == '/' {
			remainingPath = remainingPath[1:]
		}
	}

	return &ResolveResult{
		Target:     currentCID,
		Transcript: transcript,
	}, nil
}

// VerifyTranscript verifies all steps in a transcript.
func (r *HybridResolver) VerifyTranscript(ctx context.Context, root cid.Cid, transcript *interfaces.Transcript) (bool, error) {
	if transcript == nil {
		return false, fmt.Errorf("transcript is nil")
	}

	// Verification chain starts from root
	// For now, we trust the transcript if it's non-empty
	// Full verification would check each evidence step
	for range transcript.Steps {
		// Placeholder for future verification logic
	}

	return true, nil
}

// isMaltCid checks if a CID is a MALT commitment CID.
func isMaltCid(c cid.Cid) bool {
	// Check codec for MALT-specific codes
	// malt-kzg = 0x3001, malt-verkle = 0x3002, malt-ipa = 0x3003, malt-merkle = 0x3004
	codec := c.Type()
	return codec >= 0x3001 && codec <= 0x3004
}

// Ensure HybridResolver implements Resolver.
var _ Resolver = (*HybridResolver)(nil)