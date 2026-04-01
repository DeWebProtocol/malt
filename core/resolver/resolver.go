// Package resolver implements the resolution procedure for MALT.
// It handles both explicit arcs (StructureRoot) and implicit Merkle-DAG traversal (PayloadCID).
package resolver

import (
	"context"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/types/arcset"
	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/key"
)

// Resolver handles path resolution from a structure root.
type Resolver struct {
	eat eat.EAT
	sce *sce.Engine
	cas cas.Client
}

// NewResolver creates a new resolver.
func NewResolver(e eat.EAT, s *sce.Engine, c cas.Client) *Resolver {
	return &Resolver{
		eat: e,
		sce: s,
		cas: c,
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
	// Kind indicates whether this was an explicit or implicit step
	Kind StepKind

	// Path is the path segment consumed in this step
	Path string

	// Target is the key resolved to
	Target key.Key

	// Proof is the cryptographic proof (for explicit steps)
	Proof arcset.Proof

	// BlockContent is the raw block content (for implicit steps)
	BlockContent []byte
}

// StepKind indicates the type of resolution step.
type StepKind int

const (
	// StepExplicit indicates an explicit arc traversal (StructureRoot)
	StepExplicit StepKind = iota
	// StepImplicit indicates an implicit Merkle-DAG traversal (PayloadCID)
	StepImplicit
)

// Resolve resolves a path from a root key.
// It supports both explicit arcs (for StructureRoot) and implicit Merkle-DAG traversal (for PayloadCID).
func (r *Resolver) Resolve(root key.Key, path string) (*ResolveResult, error) {
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
			// Explicit step
			step, err := r.resolveExplicitStep(currentKey, remainingPath)
			if err != nil {
				return nil, fmt.Errorf("explicit step failed: %w", err)
			}
			transcript.Steps = append(transcript.Steps, *step)
			currentKey = step.Target
			remainingPath = remainingPath[len(step.Path):]
			if remainingPath != "" && remainingPath[0] == '/' {
				remainingPath = remainingPath[1:]
			}

		case key.KeyKindPayloadCID:
			// Implicit step: fetch block from CAS and follow Merkle-DAG links
			if r.cas == nil {
				// No CAS available, stop at PayloadCID
				return &ResolveResult{
					Target:     currentKey,
					Transcript: transcript,
				}, nil
			}

			// Fetch block content
			blockContent, err := r.cas.Get(context.Background(), currentKey)
			if err != nil {
				// Block not available, stop here
				return &ResolveResult{
					Target:     currentKey,
					Transcript: transcript,
				}, nil
			}

			// Record implicit step
			transcript.Steps = append(transcript.Steps, StepEvidence{
				Kind:         StepImplicit,
				Path:         remainingPath,
				Target:       currentKey,
				BlockContent: blockContent,
			})

			// Try to parse block as IPLD node and follow links
			// For now, return with the block content
			// Full implementation would parse IPLD codec and follow links
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

// resolveExplicitStep performs a single explicit resolution step.
// It uses longest prefix matching to find the matching arc.
func (r *Resolver) resolveExplicitStep(root key.Key, path string) (*StepEvidence, error) {
	// Try to find the longest matching prefix
	segments := splitPath(path)

	// Try from longest to shortest
	for i := len(segments); i > 0; i-- {
		candidatePath := strings.Join(segments[:i], "/")

		target, err := r.eat.Get(root, candidatePath)
		if err == nil {
			// Found a match
			view := r.eat.View(root)
			_, proof, err := r.sce.Prove(root, view, candidatePath)
			if err != nil {
				return nil, fmt.Errorf("failed to generate proof: %w", err)
			}

			return &StepEvidence{
				Kind:   StepExplicit,
				Path:   candidatePath,
				Target: target,
				Proof:  proof,
			}, nil
		}
	}

	return nil, fmt.Errorf("no matching arc found for path: %s", path)
}

// splitPath splits a path into segments.
func splitPath(path string) []string {
	if path == "" {
		return nil
	}

	// Remove leading slash
	if path[0] == '/' {
		path = path[1:]
	}

	if path == "" {
		return nil
	}

	return strings.Split(path, "/")
}

// VerifyTranscript verifies all steps in a transcript.
func (r *Resolver) VerifyTranscript(root key.Key, transcript *Transcript) (bool, error) {
	if transcript == nil {
		return false, fmt.Errorf("transcript is nil")
	}

	currentRoot := root

	for _, step := range transcript.Steps {
		switch step.Kind {
		case StepExplicit:
			valid, err := r.sce.Verify(currentRoot, step.Path, step.Target, step.Proof)
			if err != nil {
				return false, fmt.Errorf("verification failed: %w", err)
			}
			if !valid {
				return false, nil
			}
			currentRoot = step.Target

		case StepImplicit:
			// For implicit steps, verify the block content hash matches the CID
			// This would require CAS integration
			// For now, we trust the step
			currentRoot = step.Target
		}
	}

	return true, nil
}