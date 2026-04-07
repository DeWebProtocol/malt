// Package malt provides the application-level API for MALT.
// MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
// structures on top of content-addressed storage.
package malt

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
	cid "github.com/ipfs/go-cid"
)

// Structure represents a MALT structure with explicit arcs.
type Structure struct {
	root     cid.Cid
	bucketId string
	eat      eat.EAT
	sce      *sce.Engine
}

// NewStructure creates a new structure from an arc set.
func NewStructure(arcs arcset.Snapshot, bucketId string, e eat.EAT, s *sce.Engine) (*Structure, error) {
	// Generate commitment
	root, err := s.Commit(arcs)
	if err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Collect arcs into a map
	arcsMap := make(map[string]cid.Cid)
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		arcsMap[path] = target
	}
	if iter.Err() != nil {
		return nil, fmt.Errorf("iteration error: %w", iter.Err())
	}

	// Store arcs in EAT using Update (first version, no old root)
	if err := e.Update(bucketId, root, cid.Undef, arcsMap); err != nil {
		return nil, fmt.Errorf("failed to store arcs: %w", err)
	}

	return &Structure{
		root:     root,
		bucketId: bucketId,
		eat:      e,
		sce:      s,
	}, nil
}

// Root returns the structure root (commitment CID).
func (s *Structure) Root() cid.Cid {
	return s.root
}

// Resolve resolves a path from the structure root.
// Returns the target CID and a proof.
func (s *Structure) Resolve(path string) (cid.Cid, []byte, error) {
	// Get target from EAT
	target, err := s.eat.Get(s.bucketId, s.root, path)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to get arc: %w", err)
	}

	// Generate proof
	snapshot := s.eat.Snapshot(s.bucketId, s.root)
	_, proof, err := s.sce.Prove(s.root, snapshot, path)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("failed to generate proof: %w", err)
	}

	return target, proof, nil
}

// Update updates an arc in the structure.
// Returns a new Structure with the updated arc.
func (s *Structure) Update(path string, newKey cid.Cid) (*Structure, error) {
	// Get current value
	oldKey, err := s.eat.Get(s.bucketId, s.root, path)
	if err != nil {
		return nil, fmt.Errorf("failed to get current value: %w", err)
	}

	// Update commitment
	snapshot := s.eat.Snapshot(s.bucketId, s.root)
	newRoot, err := s.sce.Update(s.root, snapshot, path, oldKey, newKey)
	if err != nil {
		return nil, fmt.Errorf("failed to update commitment: %w", err)
	}

	// Update EAT
	arcsMap := map[string]cid.Cid{path: newKey}
	if err := s.eat.Update(s.bucketId, newRoot, s.root, arcsMap); err != nil {
		return nil, fmt.Errorf("failed to update EAT: %w", err)
	}

	return &Structure{
		root:     newRoot,
		bucketId: s.bucketId,
		eat:      s.eat,
		sce:      s.sce,
	}, nil
}

// Verify verifies a proof for an arc.
func (s *Structure) Verify(path string, target cid.Cid, proof []byte) (bool, error) {
	return s.sce.Verify(s.root, path, target, proof)
}

// GetArcSet returns a Snapshot for this structure.
func (s *Structure) GetArcSet() arcset.Snapshot {
	return s.eat.Snapshot(s.bucketId, s.root)
}