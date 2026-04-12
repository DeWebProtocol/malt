// Package interfaces defines the core interfaces for MALT architecture.
package interfaces

import (
	"context"

	cid "github.com/ipfs/go-cid"
)

// Deployment is the factory for creating Graph instances.
// It injects the storage backends and commitment scheme,
// allowing different deployment configurations (memory, IPFS, sidecar).
//
// Design principle: Deployment is the composition root.
// It wires up all dependencies and creates the Graph.
type Deployment interface {
	// CreateGraph creates a new Graph instance with this deployment's configuration.
	// The returned Graph uses the injected ArcStore, ContentStore, and CommitmentBackend.
	CreateGraph() (Graph, error)

	// ArcStore returns the ArcStore used by this deployment.
	ArcStore() ArcStore

	// ContentStore returns the ContentStore used by this deployment.
	ContentStore() ContentStore

	// CommitmentBackend returns the CommitmentBackend used by this deployment.
	CommitmentBackend() CommitmentBackend

	// InitializeGraph creates a new empty graph and returns its initial root.
	// The root is a commitment to an empty arc set.
	InitializeGraph(ctx context.Context) (cid.Cid, error)

	// Name returns the deployment name (e.g., "memory", "ipfs", "sidecar").
	Name() string

	// Close releases all resources.
	Close() error
}