// Package arctable defines the Explicit Arc Table interface and implementations.
// ArcTable is an internal component for fast lookup of arc targets.
// It provides NO correctness guarantee; verification belongs to the semantic
// layer and its commitment backend.
package arctable

import (
	"context"
	"errors"

	"github.com/dewebprotocol/malt/core/arctable/bloom"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// IsNotFound checks if an error represents an arc-not-found condition.
// It covers both arctable and arcset not-found errors.
func IsNotFound(err error) bool {
	return errors.Is(err, arcset.ErrNotFound)
}

// ArcTable (Explicit Arc Table) stores arc entries for fast lookup.
// It maps (bucketId, path) -> target CID.
// bucketId provides namespace isolation for different graphs.
// Both versioned and non-versioned implementations share this interface.
type ArcTable interface {
	// Get retrieves the target CID for (bucketId, root, path).
	// bucketId is the namespace for the arc set.
	// For overwrite ArcTable: root is optional (cid.Undef skips validation).
	// For versioned ArcTable: root is the version to start the chain lookup.
	// Returns ErrNotFound if not found.
	Get(ctx context.Context, bucketId string, root cid.Cid, path arcset.Path) (cid.Cid, error)

	// BatchGet retrieves multiple target CIDs in a single operation.
	// Returns a map of path -> CID for paths that were found.
	// Paths not found are omitted from the result map (no error).
	BatchGet(ctx context.Context, bucketId string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error)

	// Update stores arc entries with a new commitment root.
	// bucketId is the namespace for the arc set.
	// For overwrite ArcTable: oldRoot mappings are invalidated, data is overwritten.
	// For versioned ArcTable: newRoot is linked to parentRoot via @previous.
	// Use cid.Undef for oldRoot/parentRoot for the first version.
	// If a target CID is cid.Undef, the corresponding arc is deleted.
	Update(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error

	// Snapshot returns an immutable snapshot of all arcs for a given root.
	// The snapshot preloads all data into memory, suitable for random access.
	// For overwrite ArcTable: root is optional (cid.Undef skips validation).
	// For versioned ArcTable: includes all ancestor arcs via @previous chain.
	Snapshot(ctx context.Context, bucketId string, root cid.Cid) (arcset.ArcSet, error)

	// Iterate returns a streaming iterator over arcs for a given root.
	// For overwrite ArcTable: root is optional (cid.Undef skips validation).
	// For versioned ArcTable: root is the version to iterate (walks @previous chain).
	// Caller must call Close() on the iterator when done.
	Iterate(ctx context.Context, bucketId string, root cid.Cid) arcset.Iterator

	// Close releases resources.
	Close() error
}

// BucketCreator is an optional interface for ArcTable implementations
// that support creating buckets with custom bloom filter configuration.
type BucketCreator interface {
	// CreateBucket creates a new bucket with custom bloom configuration.
	// If cfg is nil, default configuration is used.
	CreateBucket(ctx context.Context, bucketId string, cfg *bloom.BucketConfig) error
}
