// Package implicit implements the Resolver interface for Merkle-DAG traversal.
// It fetches blocks from CAS and follows IPLD links.
package implicit

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/key"
)

// Resolver resolves implicit Merkle-DAG links via CAS.
type Resolver struct {
	cas cas.Client
}

// NewResolver creates a new implicit resolver.
func NewResolver(c cas.Client) *Resolver {
	return &Resolver{
		cas: c,
	}
}

// Resolve fetches the block for a PayloadCID and returns it as evidence.
// For implicit resolution, the "matched path" is the entire remaining path
// since we're at a leaf node (PayloadCID) in the MALT structure.
func (r *Resolver) Resolve(root key.Key, path string) (matchedPath string, target key.Key, ev evidence.Evidence, err error) {
	if root == nil {
		return "", nil, nil, fmt.Errorf("root is nil")
	}

	// Only handle PayloadCID
	if root.Kind() != key.KeyKindPayloadCID {
		return "", nil, nil, fmt.Errorf("implicit resolver only handles PayloadCID, got %v", root.Kind())
	}

	if r.cas == nil {
		return "", nil, nil, fmt.Errorf("CAS client is nil")
	}

	// Fetch block from CAS
	blockContent, err := r.cas.Get(context.Background(), root)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to fetch block: %w", err)
	}

	// For implicit resolution, we return the block content as evidence
	// The matched path is the entire remaining path (we consume it all)
	return path, root, evidence.NewImplicitEvidence(blockContent), nil
}

// Verify verifies the evidence for an implicit step.
// It checks that the block content hash matches the CID.
func (r *Resolver) Verify(root key.Key, path string, target key.Key, ev evidence.Evidence) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("evidence is nil")
	}

	implicitEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		return false, fmt.Errorf("expected ImplicitEvidence, got %T", ev)
	}

	// Verify block content hash matches the CID
	// Recompute CID from block content
	blockContent := implicitEv.Bytes()
	computedCID, err := key.NewPayloadCID(blockContent)
	if err != nil {
		return false, fmt.Errorf("failed to compute CID: %w", err)
	}

	// Check if the root matches
	if !root.Equals(computedCID) {
		return false, nil
	}

	return true, nil
}