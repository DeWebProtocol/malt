// Package eat defines the Explicit Arc Table interface and implementations.
// EAT is an internal component for fast lookup of arc targets.
// It provides NO correctness guarantee - SCE is responsible for verification.
package eat

import (
	"context"
	"errors"

	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// IsNotFound checks if an error represents an arc-not-found condition.
// It covers both eat and arcset not-found errors.
func IsNotFound(err error) bool {
	return errors.Is(err, arcset.ErrNotFound)
}

// EAT (Explicit Arc Table) stores arc entries for fast lookup.
// It maps (bucketId, path) -> target CID.
// bucketId provides namespace isolation for different graphs.
// Both versioned and non-versioned implementations share this interface.
type EAT interface {
	// Get retrieves the target CID for (bucketId, root, path).
	// bucketId is the namespace for the arc set.
	// For overwrite EAT: root is optional (cid.Undef skips validation).
	// For versioned EAT: root is the version to start the chain lookup.
	// Returns ErrNotFound if not found.
	Get(ctx context.Context, bucketId string, root cid.Cid, path string) (cid.Cid, error)

	// BatchGet retrieves multiple target CIDs in a single operation.
	// Returns a map of path -> CID for paths that were found.
	// Paths not found are omitted from the result map (no error).
	BatchGet(ctx context.Context, bucketId string, root cid.Cid, paths []string) (map[string]cid.Cid, error)

	// Update stores arc entries with a new commitment root.
	// bucketId is the namespace for the arc set.
	// For overwrite EAT: oldRoot mappings are invalidated, data is overwritten.
	// For versioned EAT: newRoot is linked to parentRoot via @previous.
	// Use cid.Undef for oldRoot/parentRoot for the first version.
	// If a target CID is cid.Undef, the corresponding arc is deleted.
	Update(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid, arcs map[string]cid.Cid) error

	// Snapshot returns an immutable snapshot of all arcs for a given root.
	// The snapshot preloads all data into memory, suitable for random access.
	// For overwrite EAT: root is optional (cid.Undef skips validation).
	// For versioned EAT: includes all ancestor arcs via @previous chain.
	Snapshot(ctx context.Context, bucketId string, root cid.Cid) (arcset.ArcSet, error)

	// Iterate returns a streaming iterator over arcs for a given root.
	// For overwrite EAT: root is optional (cid.Undef skips validation).
	// For versioned EAT: root is the version to iterate (walks @previous chain).
	// Caller must call Close() on the iterator when done.
	Iterate(ctx context.Context, bucketId string, root cid.Cid) arcset.Iterator

	// Close releases resources.
	Close() error
}

// BucketCreator is an optional interface for EAT implementations
// that support creating buckets with custom bloom filter configuration.
type BucketCreator interface {
	// CreateBucket creates a new bucket with custom bloom configuration.
	// If cfg is nil, default configuration is used.
	CreateBucket(ctx context.Context, bucketId string, cfg *bloom.BucketConfig) error
}
