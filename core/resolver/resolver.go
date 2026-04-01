// Package resolver implements single-step MALT resolution.
// It finds the longest matching prefix in EAT and generates cryptographic proof.
// For full path resolution with prefix consumption, see the gateway package.
package resolver

import (
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/key"
)

// Resolver handles single-step resolution from EAT.
// It uses longest-prefix matching to find arcs and generates proofs via SCE.
type Resolver struct {
	eat eat.EAT
	sce *sce.Engine
}

// NewResolver creates a new resolver.
func NewResolver(e eat.EAT, s *sce.Engine) *Resolver {
	return &Resolver{
		eat: e,
		sce: s,
	}
}

// ResolveStep finds the longest matching prefix in the EAT and generates proof.
// Returns: matchedPath, target, proof, error
//
// Example: if EAT contains "a/b/c" → key1 and path is "a/b/c/d/e",
// it matches "a/b/c" and returns that path with its target and proof.
func (r *Resolver) ResolveStep(root key.Key, path string) (matchedPath string, target key.Key, proof arcset.Proof, err error) {
	if root == nil {
		return "", nil, nil, fmt.Errorf("root is nil")
	}
	if path == "" {
		return "", nil, nil, fmt.Errorf("path is empty")
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
				return "", nil, nil, fmt.Errorf("failed to generate proof: %w", err)
			}

			return candidatePath, target, proof, nil
		}
	}

	return "", nil, nil, fmt.Errorf("no matching arc found for path: %s", path)
}

// VerifyStep verifies a single step's proof.
func (r *Resolver) VerifyStep(root key.Key, path string, target key.Key, proof arcset.Proof) (bool, error) {
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