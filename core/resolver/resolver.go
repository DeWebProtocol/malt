// Package resolver defines the Resolver interface for single-step resolution.
// Different implementations handle different resolution strategies:
// - explicit: MALT explicit arc resolution
// - implicit: Merkle-DAG implicit resolution
// - hamt: HAMT-based resolution
package resolver

import (
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// Resolver resolves a single step from a root CID.
// It finds the longest matching prefix and returns evidence.
type Resolver interface {
	// Resolve finds the longest matching prefix and returns evidence.
	// Returns: matchedPath, target, evidence, error
	Resolve(root cid.Cid, path string) (matchedPath string, target cid.Cid, ev evidence.Evidence, err error)

	// Verify verifies the evidence for a resolution step.
	Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error)
}

// ResolverFunc is a function type that implements Resolver.
// Useful for composing resolvers.
type ResolverFunc func(root cid.Cid, path string) (string, cid.Cid, evidence.Evidence, error)

// Resolve implements Resolver.
func (f ResolverFunc) Resolve(root cid.Cid, path string) (string, cid.Cid, evidence.Evidence, error) {
	return f(root, path)
}

// Verify implements Resolver with a default no-op.
func (f ResolverFunc) Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error) {
	return true, nil
}