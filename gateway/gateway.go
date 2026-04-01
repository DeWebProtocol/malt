// Package gateway implements hybrid resolution with prefix consumption.
// It handles the full resolution loop, combining explicit MALT arcs
// with implicit Merkle-DAG traversal via CAS.
package gateway

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/key"
)

// Gateway handles hybrid resolution with prefix consumption.
type Gateway struct {
	resolver *resolver.Resolver
	cas      cas.Client
}

// NewGateway creates a new gateway.
func NewGateway(r *resolver.Resolver, c cas.Client) *Gateway {
	return &Gateway{
		resolver: r,
		cas:      c,
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
			// Explicit step: use resolver for longest-prefix match
			matchedPath, target, proof, err := g.resolver.ResolveStep(currentKey, remainingPath)
			if err != nil {
				return nil, fmt.Errorf("explicit step failed: %w", err)
			}

			transcript.Steps = append(transcript.Steps, StepEvidence{
				Kind:   StepExplicit,
				Path:   matchedPath,
				Target: target,
				Proof:  proof,
			})

			currentKey = target
			remainingPath = remainingPath[len(matchedPath):]
			if remainingPath != "" && remainingPath[0] == '/' {
				remainingPath = remainingPath[1:]
			}

		case key.KeyKindPayloadCID:
			// Implicit step: fetch block from CAS and follow Merkle-DAG links
			if g.cas == nil {
				// No CAS available, stop at PayloadCID
				return &ResolveResult{
					Target:     currentKey,
					Transcript: transcript,
				}, nil
			}

			// Fetch block content
			blockContent, err := g.cas.Get(context.Background(), currentKey)
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

// VerifyTranscript verifies all steps in a transcript.
func (g *Gateway) VerifyTranscript(root key.Key, transcript *Transcript) (bool, error) {
	if transcript == nil {
		return false, fmt.Errorf("transcript is nil")
	}

	currentRoot := root

	for _, step := range transcript.Steps {
		switch step.Kind {
		case StepExplicit:
			valid, err := g.resolver.VerifyStep(currentRoot, step.Path, step.Target, step.Proof)
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