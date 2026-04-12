// Package interfaces defines the core interfaces for MALT architecture.
package interfaces

import (
	"context"

	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ArcStore provides storage for explicit arcs (path → CID mappings).
// Arcs are scoped by a root CID, allowing multiple graph versions
// to coexist in the same store.
//
// Design principle: root is always passed as parameter (stateless).
// The store may use bucket-based isolation internally (like EAT).
type ArcStore interface {
	// Get retrieves the target CID for a path under a given root.
	// Returns arcset.ErrNotFound if the path doesn't exist.
	Get(ctx context.Context, root cid.Cid, path string) (cid.Cid, error)

	// BatchGet retrieves multiple target CIDs for paths under a root.
	// Returns a map of path -> CID for paths that were found.
	// Paths not found are omitted from the result map.
	BatchGet(ctx context.Context, root cid.Cid, paths []string) (map[string]cid.Cid, error)

	// Put stores an arc (path → CID) under a given root.
	// This is typically used during graph updates.
	Put(ctx context.Context, root cid.Cid, path string, target cid.Cid) error

	// BatchPut stores multiple arcs under a given root.
	// This is used for batch updates to minimize write amplification.
	BatchPut(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) error

	// Delete removes an arc under a given root.
	Delete(ctx context.Context, root cid.Cid, path string) error

	// Snapshot returns an immutable view of all arcs under a root.
	// This is used to generate commitments.
	Snapshot(ctx context.Context, root cid.Cid) (arcset.Snapshot, error)

	// Iterate returns an iterator over all arcs under a root.
	// This is used for streaming arc traversal.
	Iterate(ctx context.Context, root cid.Cid) arcset.Iterator

	// Size returns the number of arcs under a root.
	Size(ctx context.Context, root cid.Cid) (int, error)

	// Close releases resources.
	Close() error
}