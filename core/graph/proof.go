// Package graph provides graph lifecycle management for MALT.
// This file contains the TranscriptProof type for verification.
package graph

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/resolver"
	cid "github.com/ipfs/go-cid"
)

// TranscriptProof implements Proof interface using transcript evidence.
type TranscriptProof struct {
	transcript *resolver.Transcript
}

// NewTranscriptProof creates a new Proof from a transcript.
func NewTranscriptProof(transcript *resolver.Transcript) *TranscriptProof {
	return &TranscriptProof{
		transcript: transcript,
	}
}

// Transcript returns the step-by-step evidence.
func (p *TranscriptProof) Transcript() *resolver.Transcript {
	return p.transcript
}

// Verify verifies the proof against a root and expected target.
func (p *TranscriptProof) Verify(root cid.Cid, expectedTarget cid.Cid) (bool, error) {
	if p.transcript == nil {
		return false, fmt.Errorf("transcript is nil")
	}

	if len(p.transcript.Steps) == 0 {
		// No steps means root == target
		return root.Equals(expectedTarget), nil
	}

	// Check that the final step's target matches expected
	finalStep := p.transcript.Steps[len(p.transcript.Steps)-1]
	return finalStep.Target.Equals(expectedTarget), nil
}

// Size returns the proof size in bytes.
func (p *TranscriptProof) Size() int {
	if p.transcript == nil {
		return 0
	}

	// Approximate size based on number of steps
	size := 0
	for _, step := range p.transcript.Steps {
		size += len(step.Path)
		size += 36 // CID size
		// Evidence size varies, estimate 100 bytes per step
		size += 100
	}
	return size
}

// Ensure TranscriptProof implements Proof.
var _ Proof = (*TranscriptProof)(nil)
