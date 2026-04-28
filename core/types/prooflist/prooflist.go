// Package prooflist defines the verifier-facing read proof transcript shape.
package prooflist

import (
	"fmt"

	cid "github.com/ipfs/go-cid"
)

// StepKind identifies the semantic role of one ordered proof step.
type StepKind string

const (
	KindMapStep        StepKind = "map_step"
	KindPayloadBinding StepKind = "payload_binding"
	KindListIndex      StepKind = "list_index"
	KindBlobBinding    StepKind = "blob_binding"
	KindImplicitBlock  StepKind = "implicit_block"
	KindLegacyUnknown  StepKind = "legacy_unknown"
)

// ProofList is an ordered verifier-facing proof artifact for a read.
//
// It is a schema and adapter boundary only. Cryptographic verification is still
// owned by the concrete semantic backend that emitted each proof payload.
type ProofList struct {
	Root  cid.Cid `json:"root"`
	Query string  `json:"query,omitempty"`
	Steps []Step  `json:"steps"`
}

// Step records one hop from a structure root or block to a target CID.
type Step struct {
	Kind            StepKind `json:"kind"`
	From            cid.Cid  `json:"from"`
	Query           string   `json:"query,omitempty"`
	Coordinate      string   `json:"coordinate,omitempty"`
	Path            string   `json:"path,omitempty"`
	Index           *uint64  `json:"index,omitempty"`
	Target          cid.Cid  `json:"target"`
	EvidenceKind    string   `json:"evidence_kind,omitempty"`
	EvidenceBackend string   `json:"evidence_backend,omitempty"`
	Evidence        []byte   `json:"evidence,omitempty"`
	Proof           []byte   `json:"proof,omitempty"`
}

type validateConfig struct {
	requireSteps bool
}

// ValidateOption customizes shape validation.
type ValidateOption func(*validateConfig)

// RequireSteps rejects a ProofList with no ordered steps.
func RequireSteps() ValidateOption {
	return func(cfg *validateConfig) {
		cfg.requireSteps = true
	}
}

// ValidateShape checks that the ProofList is structurally usable by a verifier.
//
// This does not perform cryptographic end-to-end verification.
func (p ProofList) ValidateShape(opts ...ValidateOption) error {
	cfg := validateConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if !p.Root.Defined() {
		return fmt.Errorf("prooflist root is undefined")
	}
	if cfg.requireSteps && len(p.Steps) == 0 {
		return fmt.Errorf("prooflist steps are empty")
	}
	for i, step := range p.Steps {
		if !step.Kind.Known() {
			return fmt.Errorf("prooflist step %d has unknown kind %q", i, step.Kind)
		}
		if !step.From.Defined() {
			return fmt.Errorf("prooflist step %d from CID is undefined", i)
		}
		if !step.Target.Defined() {
			return fmt.Errorf("prooflist step %d target CID is undefined", i)
		}
	}
	return nil
}

// Known reports whether k is part of the current ProofList schema.
func (k StepKind) Known() bool {
	switch k {
	case KindMapStep, KindPayloadBinding, KindListIndex, KindBlobBinding, KindImplicitBlock, KindLegacyUnknown:
		return true
	default:
		return false
	}
}
