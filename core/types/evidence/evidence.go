// Package evidence defines the Evidence interface and its implementations.
// Evidence represents cryptographic proof of a resolution step, similar to
// how key.Key represents different key types.
package evidence

import (
	"bytes"
	"fmt"
)

// EvidenceKind represents the type of evidence.
type EvidenceKind int

const (
	// EvidenceKindExplicit indicates evidence from explicit arc resolution (MALT).
	EvidenceKindExplicit EvidenceKind = iota
	// EvidenceKindImplicit indicates evidence from implicit Merkle-DAG traversal.
	EvidenceKindImplicit
	// EvidenceKindHAMT indicates evidence from HAMT resolution.
	EvidenceKindHAMT
)

// Evidence represents proof of a resolution step.
// Different resolvers produce different evidence types.
type Evidence interface {
	// Bytes returns the raw bytes for storage/encoding.
	Bytes() []byte

	// String returns a human-readable representation.
	String() string

	// Equals checks if two evidences are equal.
	Equals(other Evidence) bool

	// Kind returns the type of the evidence.
	Kind() EvidenceKind
}

// ExplicitEvidence represents cryptographic proof emitted by the semantic layer
// over a primitive commitment backend such as KZG.
type ExplicitEvidence struct {
	proof []byte
}

// NewExplicitEvidence creates a new explicit evidence from proof bytes.
func NewExplicitEvidence(proof []byte) *ExplicitEvidence {
	return &ExplicitEvidence{proof: proof}
}

// Bytes returns the proof bytes.
func (e *ExplicitEvidence) Bytes() []byte {
	return e.proof
}

// String returns a hex representation.
func (e *ExplicitEvidence) String() string {
	return fmt.Sprintf("explicit:%x", e.proof)
}

// Equals checks equality.
func (e *ExplicitEvidence) Equals(other Evidence) bool {
	if other == nil {
		return false
	}
	o, ok := other.(*ExplicitEvidence)
	if !ok {
		return false
	}
	return bytes.Equal(e.proof, o.proof)
}

// Kind returns EvidenceKindExplicit.
func (e *ExplicitEvidence) Kind() EvidenceKind {
	return EvidenceKindExplicit
}

// ImplicitEvidence represents block content for Merkle-DAG traversal.
type ImplicitEvidence struct {
	blockContent []byte
}

// NewImplicitEvidence creates a new implicit evidence from block content.
func NewImplicitEvidence(blockContent []byte) *ImplicitEvidence {
	return &ImplicitEvidence{blockContent: blockContent}
}

// Bytes returns the block content bytes.
func (e *ImplicitEvidence) Bytes() []byte {
	return e.blockContent
}

// String returns a summary.
func (e *ImplicitEvidence) String() string {
	return fmt.Sprintf("implicit:%d bytes", len(e.blockContent))
}

// Equals checks equality.
func (e *ImplicitEvidence) Equals(other Evidence) bool {
	if other == nil {
		return false
	}
	o, ok := other.(*ImplicitEvidence)
	if !ok {
		return false
	}
	return bytes.Equal(e.blockContent, o.blockContent)
}

// Kind returns EvidenceKindImplicit.
func (e *ImplicitEvidence) Kind() EvidenceKind {
	return EvidenceKindImplicit
}

// HAMTEvidence represents proof from HAMT resolution.
type HAMTEvidence struct {
	proof []byte
}

// NewHAMTEvidence creates a new HAMT evidence from proof bytes.
func NewHAMTEvidence(proof []byte) *HAMTEvidence {
	return &HAMTEvidence{proof: proof}
}

// Bytes returns the proof bytes.
func (e *HAMTEvidence) Bytes() []byte {
	return e.proof
}

// String returns a hex representation.
func (e *HAMTEvidence) String() string {
	return fmt.Sprintf("hamt:%x", e.proof)
}

// Equals checks equality.
func (e *HAMTEvidence) Equals(other Evidence) bool {
	if other == nil {
		return false
	}
	o, ok := other.(*HAMTEvidence)
	if !ok {
		return false
	}
	return bytes.Equal(e.proof, o.proof)
}

// Kind returns EvidenceKindHAMT.
func (e *HAMTEvidence) Kind() EvidenceKind {
	return EvidenceKindHAMT
}
