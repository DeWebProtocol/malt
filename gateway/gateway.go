// Package gateway implements hybrid resolution with prefix consumption.
// It handles the full resolution loop, combining explicit MALT arcs
// with implicit Merkle-DAG traversal via CAS.
package gateway

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/key"
)

// Gateway handles hybrid resolution with prefix consumption.
// It dispatches to different resolvers based on key kind.
type Gateway struct {
	explicitResolver resolver.Resolver
	implicitResolver  resolver.Resolver
}

// NewGateway creates a new gateway with explicit and implicit resolvers.
func NewGateway(explicit, implicit resolver.Resolver) *Gateway {
	return &Gateway{
		explicitResolver: explicit,
		implicitResolver: implicit,
	}
}

// ResolveResult contains the result of a resolution operation.
type ResolveResult struct {
	// Target is the final resolved key
	Target key.Key

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

	// Target is the key resolved to
	Target key.Key

	// Evidence is the cryptographic evidence for this step
	Evidence evidence.Evidence
}

// Resolve resolves a path from a root key.
// It supports both explicit arcs (for StructureRoot) and implicit Merkle-DAG traversal (for PayloadCID).
func (g *Gateway) Resolve(root key.Key, path string) (*ResolveResult, error) {
	if root == nil {
		return nil, fmt.Errorf("root is nil")
	}

	transcript := &Transcript{Steps: make([]StepEvidence, 0)}
	currentKey := root
	remainingPath := path

	for remainingPath != "" {
		// Dispatch based on key type
		switch currentKey.Kind() {
		case key.KeyKindStructureRoot:
			// Explicit step: use explicit resolver for longest-prefix match
			matchedPath, target, ev, err := g.explicitResolver.Resolve(currentKey, remainingPath)
			if err != nil {
				return nil, fmt.Errorf("explicit resolution failed: %w", err)
			}

			// Record step
			transcript.Steps = append(transcript.Steps, StepEvidence{
				Path:     matchedPath,
				Target:   target,
				Evidence: ev,
			})

			currentKey = target

			// Update remaining path
			remainingPath = remainingPath[len(matchedPath):]
			if remainingPath != "" && remainingPath[0] == '/' {
				remainingPath = remainingPath[1:]
			}

		case key.KeyKindPayloadCID:
			// We've reached a PayloadCID (data layer)
			// Stop here - we've resolved as far as we can
			return &ResolveResult{
				Target:     currentKey,
				Transcript: transcript,
			}, nil

		default:
			return nil, fmt.Errorf("unknown key kind: %v", currentKey.Kind())
		}
	}

	return &ResolveResult{
		Target:     currentKey,
		Transcript: transcript,
	}, nil
}

// VerifyTranscript verifies all steps in a transcript.
func (g *Gateway) VerifyTranscript(root key.Key, transcript *Transcript) (bool, error) {
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