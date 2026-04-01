// Package explicit implements the Resolver interface for MALT explicit arcs.
// It uses longest-prefix matching in EAT and generates cryptographic proof via SCE.
package explicit

import (
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
	cid "github.com/ipfs/go-cid"
)

// Resolver resolves explicit MALT arcs using longest-prefix matching.
type Resolver struct {
	eat eat.EAT
	sce *sce.Engine
}

// NewResolver creates a new explicit arc resolver.
func NewResolver(e eat.EAT, s *sce.Engine) *Resolver {
	return &Resolver{
		eat: e,
		sce: s,
	}
}

// Resolve finds the longest matching prefix in the EAT and generates proof.
// Returns: matchedPath, target, evidence, error
//
// Example: if EAT contains "a/b/c" → key1 and path is "a/b/c/d/e",
// it matches "a/b/c" and returns that path with its target and evidence.
func (r *Resolver) Resolve(root cid.Cid, path string) (matchedPath string, target cid.Cid, ev evidence.Evidence, err error) {
	if !root.Defined() {
		return "", cid.Cid{}, nil, fmt.Errorf("root is not defined")
	}
	if path == "" {
		return "", cid.Cid{}, nil, fmt.Errorf("path is empty")
	}

	// Try to find the longest matching prefix
	segments := splitPath(path)

	// Try from longest to shortest
	for i := len(segments); i > 0; i-- {
		candidatePath := strings.Join(segments[:i], "/")

		target, err := r.eat.Get(root, candidatePath)
		if err == nil {
			// Found a match, generate proof
			view := r.eat.View(root)
			_, proof, err := r.sce.Prove(root, view, candidatePath)
			if err != nil {
				return "", cid.Cid{}, nil, fmt.Errorf("failed to generate proof: %w", err)
			}

			return candidatePath, target, evidence.NewExplicitEvidence(proof), nil
		}
	}

	return "", cid.Cid{}, nil, fmt.Errorf("no matching arc found for path: %s", path)
}

// Verify verifies a single step's evidence.
func (r *Resolver) Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("evidence is nil")
	}

	explicitEv, ok := ev.(*evidence.ExplicitEvidence)
	if !ok {
		return false, fmt.Errorf("expected ExplicitEvidence, got %T", ev)
	}

	// Convert evidence bytes back to arcset.Proof for SCE
	proof := arcset.Proof(explicitEv.Bytes())
	return r.sce.Verify(root, path, target, proof)
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