// Package resolver implements hybrid resolution with prefix consumption.
// It handles the full resolution loop, combining explicit MALT arcs
// with implicit Merkle-DAG traversal via CAS.
package resolver

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/resolver/step"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// Resolver handles hybrid resolution with prefix consumption.
// It dispatches to different step executors based on CID codec and continues
// traversal until the path is consumed or resolution fails.
//
// Architecture:
//   - MALT commitments (malt-kzg/malt-verkle/malt-ipa) → explicitStep
//   - All other CIDs (dag-pb, dag-cbor, raw, etc.) → implicitStep
//
// The implicitStep internally handles different data structures:
//   - UnixFS: File/directory traversal
//   - HAMT: Hash-based routing for dictionaries
//   - Plain DAG: IPLD node traversal
type Resolver struct {
	explicitStep step.Step
	implicitStep  step.Step
}

// NewResolver creates a new hybrid resolver with explicit and implicit step executors.
func NewResolver(explicit, implicit step.Step) *Resolver {
	return &Resolver{
		explicitStep: explicit,
		implicitStep:  implicit,
	}
}

// ResolveResult contains the result of a resolution operation.
type ResolveResult struct {
	// Target is the final resolved CID
	Target cid.Cid

	// Transcript contains the evidence for each step
	Transcript *interfaces.Transcript
}

// Resolve resolves a path from a root CID.
// It supports both explicit arcs (for MALT commitments) and implicit Merkle-DAG traversal (for payload CIDs).
// The resolution continues until:
//   - The path is fully consumed
//   - A resolution step fails (no matching arc or link)
//   - The target cannot be resolved further
func (r *Resolver) Resolve(root cid.Cid, path string) (*ResolveResult, error) {
	if !root.Defined() {
		return nil, ErrUndefinedRoot
	}

	transcript := &interfaces.Transcript{Steps: make([]interfaces.StepEvidence, 0)}
	currentCID := root
	remainingPath := path

	for remainingPath != "" {
		var matchedPath string
		var target cid.Cid
		var ev evidence.Evidence
		var err error

		// Dispatch based on CID codec
		if codec.IsMaltCid(currentCID) {
			// Explicit step: use explicit step executor for longest-prefix match
			if r.explicitStep == nil {
				return &ResolveResult{
					Target:     currentCID,
					Transcript: transcript,
				}, nil
			}
			matchedPath, target, ev, err = r.explicitStep.Resolve(currentCID, remainingPath)
		} else {
			// Implicit step: use implicit step executor for DAG traversal
			// The implicit step internally handles UnixFS, HAMT, and plain DAG structures
			if r.implicitStep == nil {
				return &ResolveResult{
					Target:     currentCID,
					Transcript: transcript,
				}, nil
			}
			matchedPath, target, ev, err = r.implicitStep.Resolve(currentCID, remainingPath)
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrResolutionFailed, err)
		}

		// If no path was matched, we can't continue
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

	// When path is exhausted, check if current CID is a MALT commitment.
	// If so, try to resolve @payload to materialize the object's content.
	if codec.IsMaltCid(currentCID) && r.explicitStep != nil {
		_, target, ev, err := r.explicitStep.Resolve(currentCID, explicit.PayloadArc)
		if err == nil {
			// Has @payload arc, record this step and return payload CID
			transcript.Steps = append(transcript.Steps, interfaces.StepEvidence{
				Path:     explicit.PayloadArc,
				Target:   target,
				Evidence: ev,
			})
			return &ResolveResult{
				Target:     target,
				Transcript: transcript,
			}, nil
		}
		// No @payload arc (structure-only node), fall through to return structure root
	}

	return &ResolveResult{
		Target:     currentCID,
		Transcript: transcript,
	}, nil
}

// VerifyTranscript verifies all steps in a transcript.
func (r *Resolver) VerifyTranscript(root cid.Cid, transcript *interfaces.Transcript) (bool, error) {
	if transcript == nil {
		return false, ErrTranscriptNil
	}

	currentRoot := root

	for _, stepEv := range transcript.Steps {
		var s step.Step
		switch stepEv.Evidence.Kind() {
		case evidence.EvidenceKindExplicit:
			s = r.explicitStep
		case evidence.EvidenceKindImplicit, evidence.EvidenceKindHAMT:
			// Both implicit and HAMT evidence are verified by implicit step
			// (HAMT is detected and handled inside the implicit step)
			s = r.implicitStep
		default:
			return false, fmt.Errorf("%w: %v", ErrUnknownEvidenceKind, stepEv.Evidence.Kind())
		}

		if s == nil {
			return false, fmt.Errorf("%w for evidence kind: %v", ErrStepExecutorNotAvailable, stepEv.Evidence.Kind())
		}

		valid, err := s.Verify(currentRoot, stepEv.Path, stepEv.Target, stepEv.Evidence)
		if err != nil {
			return false, fmt.Errorf("verification failed: %w", err)
		}
		if !valid {
			return false, nil
		}
		currentRoot = stepEv.Target
	}

	return true, nil
}