// Package step defines the Step interface for single-step resolution.
// Different implementations handle different resolution strategies:
// - explicit: MALT explicit arc resolution
// - implicit: Merkle-DAG implicit resolution
// - hamt: HAMT-based resolution
package step

import (
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// Step resolves a single step from a root CID.
// It finds the longest matching prefix and returns evidence.
type Step interface {
	// Resolve finds the longest matching prefix and returns evidence.
	// Returns: matchedPath, target, evidence, error
	Resolve(root cid.Cid, path string) (matchedPath string, target cid.Cid, ev evidence.Evidence, err error)

	// Verify verifies the evidence for a resolution step.
	Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error)
}

