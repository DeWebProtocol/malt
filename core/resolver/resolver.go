// Package resolver implements the MALT resolution loop with prefix consumption.
// Native explicit-arc resolution is the primary path. Ordinary Merkle/IPLD
// traversal is used as an interoperability path when resolution crosses into
// legacy CID space.
package resolver

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/resolver/step"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// Resolver handles resolution with prefix consumption. It dispatches to
// different step executors based on CID codec and continues traversal until the
// path is consumed or resolution fails.
//
// Architecture:
//   - typed MALT roots (map/list) → explicitStep
//   - All other CIDs (dag-pb, dag-cbor, raw, etc.) → implicitStep
//
// The implicitStep internally handles different data structures:
//   - UnixFS: File/directory traversal
//   - HAMT: Hash-based routing for dictionaries
//   - Plain DAG: IPLD node traversal
type Resolver struct {
	explicitStep step.Step
	implicitStep step.Step
}

// NewResolver creates a new resolver with explicit and implicit step executors.
func NewResolver(explicit, implicit step.Step) *Resolver {
	return &Resolver{
		explicitStep: explicit,
		implicitStep: implicit,
	}
}

// ResolveResult contains the result of a resolution operation.
type ResolveResult struct {
	// Target is the final resolved CID
	Target cid.Cid

	// RemainingPath is non-empty when resolution stopped before consuming the
	// requested path.
	RemainingPath arcset.Path

	// Transcript contains the evidence for each step
	Transcript *Transcript
}

// ResolveKey resolves a path to its terminal typed key without terminal payload
// materialization. For list roots, traversal is terminal (no auto @payload).
func (r *Resolver) ResolveKey(root cid.Cid, path string) (*ResolveResult, error) {
	if !root.Defined() {
		return nil, ErrUndefinedRoot
	}

	transcript := &Transcript{Steps: make([]StepEvidence, 0)}
	currentCID := root
	remainingPath := arcset.CanonicalizePath(path)

	for !remainingPath.IsEmpty() {
		// Typed list roots are terminal for path traversal.
		if codec.SemanticKindOf(currentCID) == codec.SemanticKindList {
			return &ResolveResult{
				Target:        currentCID,
				RemainingPath: remainingPath,
				Transcript:    transcript,
			}, nil
		}

		var matchedPath arcset.Path
		var target cid.Cid
		var ev evidence.Evidence
		var err error

		// Dispatch based on key kind.
		if codec.IsMaltCid(currentCID) {
			// Explicit step: use explicit step executor for longest-prefix match.
			if r.explicitStep == nil {
				return &ResolveResult{
					Target:        currentCID,
					RemainingPath: remainingPath,
					Transcript:    transcript,
				}, nil
			}
			matchedPath, target, ev, err = r.explicitStep.Resolve(currentCID, remainingPath)
		} else {
			// Implicit step: use implicit step executor for DAG traversal.
			if r.implicitStep == nil {
				return &ResolveResult{
					Target:        currentCID,
					RemainingPath: remainingPath,
					Transcript:    transcript,
				}, nil
			}
			matchedPath, target, ev, err = r.implicitStep.Resolve(currentCID, remainingPath)
		}

		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrResolutionFailed, err)
		}

		// If no path was matched, we can't continue.
		if matchedPath.IsEmpty() {
			return &ResolveResult{
				Target:        currentCID,
				RemainingPath: remainingPath,
				Transcript:    transcript,
			}, nil
		}

		// Record step.
		transcript.Steps = append(transcript.Steps, StepEvidence{
			Path:     matchedPath,
			Target:   target,
			Evidence: ev,
		})

		// Update current CID.
		currentCID = target

		// Update remaining path.
		nextPath, ok := remainingPath.Consume(matchedPath)
		if !ok {
			return nil, fmt.Errorf("%w: failed to consume matched path %q from %q", ErrResolutionFailed, matchedPath.String(), remainingPath.String())
		}
		remainingPath = nextPath
	}

	return &ResolveResult{
		Target:        currentCID,
		RemainingPath: remainingPath,
		Transcript:    transcript,
	}, nil
}

// Resolve resolves a path from a root CID.
// It supports both explicit arcs (for MALT commitments) and implicit Merkle-DAG traversal (for payload CIDs).
// The resolution continues until:
//   - The path is fully consumed
//   - A resolution step fails (no matching arc or link)
//   - The target cannot be resolved further
func (r *Resolver) Resolve(root cid.Cid, path string) (*ResolveResult, error) {
	keyResult, err := r.ResolveKey(root, path)
	if err != nil {
		return nil, err
	}
	if !keyResult.RemainingPath.IsEmpty() {
		return keyResult, nil
	}

	// Terminal materialization is map-only. List roots must remain terminal.
	if codec.SemanticKindOf(keyResult.Target) == codec.SemanticKindMap && r.explicitStep != nil {
		_, target, ev, err := r.explicitStep.Resolve(keyResult.Target, explicit.PayloadArc)
		if err != nil {
			return nil, fmt.Errorf("%w: map root %s is missing mandatory @payload binding: %w", ErrResolutionFailed, keyResult.Target.String(), err)
		}
		keyResult.Transcript.Steps = append(keyResult.Transcript.Steps, StepEvidence{
			Path:     explicit.PayloadArc,
			Target:   target,
			Evidence: ev,
		})
		return &ResolveResult{
			Target:        target,
			RemainingPath: "",
			Transcript:    keyResult.Transcript,
		}, nil
	}

	return keyResult, nil
}

// VerifyTranscript verifies all steps in a transcript.
func (r *Resolver) VerifyTranscript(root cid.Cid, transcript *Transcript) (bool, error) {
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
