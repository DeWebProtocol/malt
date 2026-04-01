// Package gateway implements hybrid resolution with prefix consumption.
// It handles the full resolution loop, combining explicit MALT arcs
// with implicit Merkle-DAG traversal via CAS.
package gateway

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// Gateway handles hybrid resolution with prefix consumption.
// It dispatches to different resolvers based on CID codec and continues
// traversal until the path is consumed or resolution fails.
type Gateway struct {
	explicitResolver resolver.Resolver
	implicitResolver  resolver.Resolver
}

// NewGateway creates a new gateway with explicit and implicit resolvers.
func NewGateway(explicit, implicit resolver.Resolver) *Gateway {
	return &Gateway{
		explicitResolver: explicit,
		implicitResolver:  implicit,
	}
}

// ResolveResult contains the result of a resolution operation.
type ResolveResult struct {
	// Target is the final resolved CID
	Target cid.Cid

	// Transcript contains the evidence for each step
	Transcript *Transcript
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

// Resolve resolves a path from a root CID.
// It supports both explicit arcs (for MALT commitments) and implicit Merkle-DAG traversal (for payload CIDs).
// The resolution continues until:
//   - The path is fully consumed
//   - A resolution step fails (no matching arc or link)
//   - The target cannot be resolved further
func (g *Gateway) Resolve(root cid.Cid, path string) (*ResolveResult, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root is not defined")
	}

	transcript := &Transcript{Steps: make([]StepEvidence, 0)}
	currentCID := root
	remainingPath := path

	for remainingPath != "" {
		var matchedPath string
		var target cid.Cid
		var ev evidence.Evidence
		var err error

		// Dispatch based on CID codec
		if codec.IsMaltCid(currentCID) {
			// Explicit step: use explicit resolver for longest-prefix match
			if g.explicitResolver == nil {
				return &ResolveResult{
					Target:     currentCID,
					Transcript: transcript,
				}, nil
			}
			matchedPath, target, ev, err = g.explicitResolver.Resolve(currentCID, remainingPath)
		} else {
			// Implicit step: use implicit resolver for DAG traversal
			if g.implicitResolver == nil {
				return &ResolveResult{
					Target:     currentCID,
					Transcript: transcript,
				}, nil
			}
			matchedPath, target, ev, err = g.implicitResolver.Resolve(currentCID, remainingPath)
		}

		if err != nil {
			return nil, fmt.Errorf("resolution failed: %w", err)
		}

		// If no path was matched, we can't continue
		if matchedPath == "" {
			return &ResolveResult{
				Target:     currentCID,
				Transcript: transcript,
			}, nil
		}

		// Record step
		transcript.Steps = append(transcript.Steps, StepEvidence{
			Path:     matchedPath,
			Target:   target,
			Evidence: ev,
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
func (g *Gateway) VerifyTranscript(root cid.Cid, transcript *Transcript) (bool, error) {
	if transcript == nil {
		return false, fmt.Errorf("transcript is nil")
	}

	currentRoot := root

	for _, step := range transcript.Steps {
		var r resolver.Resolver
		switch step.Evidence.Kind() {
		case evidence.EvidenceKindExplicit:
			r = g.explicitResolver
		case evidence.EvidenceKindImplicit:
			r = g.implicitResolver
		default:
			return false, fmt.Errorf("unknown evidence kind: %v", step.Evidence.Kind())
		}

		valid, err := r.Verify(currentRoot, step.Path, step.Target, step.Evidence)
		if err != nil {
			return false, fmt.Errorf("verification failed: %w", err)
		}
		if !valid {
			return false, nil
		}
		currentRoot = step.Target
	}

	return true, nil
}