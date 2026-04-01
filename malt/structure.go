// Package malt provides the application-level API for MALT.
// MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
// structures on top of content-addressed storage.
package malt

import (
	"fmt"

	"github.com/dewebprotocol/malt/arcset"
	"github.com/dewebprotocol/malt/internal/eat"
	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
)

// Structure represents a MALT structure with explicit arcs.
type Structure struct {
	root key.Key
	eat  eat.EAT
	sce  *sce.Engine
}

// NewStructure creates a new structure from an arc set.
func NewStructure(arcs arcset.View, e eat.EAT, s *sce.Engine) (*Structure, error) {
	// Generate commitment
	root, err := s.Commit(arcs)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Store arcs in EAT
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		if err := e.Put(root, path, target); err != nil {
			return nil, fmt.Errorf("failed to store arc: %w", err)
		}
	}
	if iter.Err() != nil {
		return nil, fmt.Errorf("iteration error: %w", iter.Err())
	}

	return &Structure{
		root: root,
		eat:  e,
		sce:  s,
	}, nil
}

// Root returns the structure root (commitment).
func (s *Structure) Root() key.Key {
	return s.root
}

// Resolve resolves a path from the structure root.
// Returns the target key and a proof.
func (s *Structure) Resolve(path string) (key.Key, arcset.Proof, error) {
	// Get target from EAT
	target, err := s.eat.Get(s.root, path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get arc: %w", err)
	}

	// Generate proof
	view := s.eat.View(s.root)
	_, proof, err := s.sce.Prove(s.root, view, path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate proof: %w", err)
	}

	return target, proof, nil
}

// Update updates an arc in the structure.
// Returns a new Structure with the updated arc.
func (s *Structure) Update(path string, newKey key.Key) (*Structure, error) {
	// Get current value
	oldKey, err := s.eat.Get(s.root, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get current value: %w", err)
	}

	// Update commitment
	view := s.eat.View(s.root)
	newRoot, err := s.sce.Update(s.root, view, path, oldKey, newKey)
	if err != nil {
		return nil, fmt.Errorf("failed to update commitment: %w", err)
	}

	// Update EAT
	if err := s.eat.Put(newRoot, path, newKey); err != nil {
		return nil, fmt.Errorf("failed to update EAT: %w", err)
	}

	return &Structure{
		root: newRoot,
		eat:  s.eat,
		sce:  s.sce,
	}, nil
}

// Verify verifies a proof for an arc.
func (s *Structure) Verify(path string, target key.Key, proof arcset.Proof) (bool, error) {
	return s.sce.Verify(s.root, path, target, proof)
}

// GetArcSet returns an ArcSetView for this structure.
func (s *Structure) GetArcSet() arcset.View {
	return s.eat.View(s.root)
}