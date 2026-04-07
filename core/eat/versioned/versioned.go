// Package versioned provides a versioned EAT implementation using a KVStore.
// Each version stores only modified arcs plus a @previous arc pointing to the parent version.
// Resolution walks the @previous chain to find arc entries.
//
// Concurrency: This implementation is inherently concurrency-safe because each update
// creates a new version with its own namespace (bucketId:version:path). Multiple concurrent
// writers operate on different versions without conflict. For production deployments,
// concurrency control should be handled at the interface layer if needed.
package versioned

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// Reserved arc paths
const (
	PreviousArc = "@previous" // Points to parent version's commitment root
)

// EAT is a versioned EAT implementation with bucket-based isolation.
// Each version stores only modified arcs, with @previous linking to the parent.
type EAT struct {
	kv kvstore.KVStore
}

// NewEAT creates a new versioned EAT with the given KVStore.
func NewEAT(kv kvstore.KVStore) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{kv: kv}, nil
}

// arcKey generates the storage key for a bucket, version and path.
// Format: bucketId:version:path
func arcKey(bucketId string, version cid.Cid, path string) []byte {
	return []byte(bucketId + ":" + version.String() + ":" + path)
}

// versionPrefix generates the prefix for all arcs of a specific version in a bucket.
// Format: bucketId:version:
func versionPrefix(bucketId string, version cid.Cid) []byte {
	return []byte(bucketId + ":" + version.String() + ":")
}

// Get retrieves the target CID for a path at a specific version.
// It walks the @previous chain starting from the given version until finding the arc.
// Returns ErrNotFound if the path doesn't exist in any ancestor version,
// or if a tombstone (cid.Undef) is found indicating the arc was deleted.
func (e *EAT) Get(bucketId string, version cid.Cid, path string) (cid.Cid, error) {
	ctx := context.Background()

	currentVersion := version
	maxDepth := 1000 // Prevent infinite loops

	for range maxDepth {
		// Try to get the arc at current version
		key := arcKey(bucketId, currentVersion, path)
		val, err := e.kv.Get(ctx, key)
		if err == nil {
			// Found the arc entry
			// Check if it's a tombstone (empty bytes = cid.Undef)
			if len(val) == 0 {
				// Arc was deleted at this version
				return cid.Cid{}, arcset.ErrNotFound
			}
			// Parse and return the CID
			return cid.Cast(val)
		}

		if err != kvstore.ErrNotFound {
			return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
		}

		// Arc not found at this version, try parent via @previous
		prevKey := arcKey(bucketId, currentVersion, PreviousArc)
		prevVal, err := e.kv.Get(ctx, prevKey)
		if err != nil {
			if err == kvstore.ErrNotFound {
				// No parent, arc doesn't exist
				return cid.Cid{}, arcset.ErrNotFound
			}
			return cid.Cid{}, fmt.Errorf("failed to get @previous: %w", err)
		}

		// Move to parent version
		parentVersion, err := cid.Cast(prevVal)
		if err != nil {
			return cid.Cid{}, fmt.Errorf("invalid @previous CID: %w", err)
		}
		currentVersion = parentVersion
	}

	return cid.Cid{}, fmt.Errorf("version chain too deep (max %d)", maxDepth)
}

// BatchGet retrieves multiple target CIDs in a single operation.
// Returns a map of path -> CID for paths that were found.
// Paths not found or deleted (tombstone) are omitted from the result map.
// This is more efficient than multiple Get calls as it traverses the version chain once.
func (e *EAT) BatchGet(bucketId string, version cid.Cid, paths []string) (map[string]cid.Cid, error) {
	ctx := context.Background()

	// Track which paths we still need to find
	remaining := make(map[string]bool)
	for _, path := range paths {
		remaining[path] = true
	}

	results := make(map[string]cid.Cid)
	tombstones := make(map[string]bool) // Paths marked as deleted

	currentVersion := version
	maxDepth := 1000

	for len(remaining) > 0 && maxDepth > 0 {
		maxDepth--

		// Check each remaining path at current version
		for path := range remaining {
			if tombstones[path] {
				continue // Already marked as deleted
			}

			key := arcKey(bucketId, currentVersion, path)
			val, err := e.kv.Get(ctx, key)
			if err == nil {
				// Found the arc
				if len(val) == 0 {
					// Tombstone - mark as deleted
					tombstones[path] = true
				} else if c, err := cid.Cast(val); err == nil {
					results[path] = c
				}
				delete(remaining, path)
			} else if err != kvstore.ErrNotFound {
				return nil, fmt.Errorf("failed to get arc %s: %w", path, err)
			}
		}

		// Remove tombstoned paths from remaining
		for path := range tombstones {
			delete(remaining, path)
		}

		if len(remaining) == 0 {
			break
		}

		// Move to parent version
		prevKey := arcKey(bucketId, currentVersion, PreviousArc)
		prevVal, err := e.kv.Get(ctx, prevKey)
		if err != nil {
			break // No parent, remaining paths not found
		}

		parentVersion, err := cid.Cast(prevVal)
		if err != nil {
			break
		}
		currentVersion = parentVersion
	}

	return results, nil
}

// Update stores arcs at a new version and links it to the parent version.
// The newRoot becomes the new version identifier, linked to parentRoot via @previous.
// If parentRoot is cid.Undef, this creates the first version (no @previous).
// If a target CID is cid.Undef, a tombstone (empty bytes) is stored to indicate deletion.
// When Get() encounters a tombstone while walking the chain, it returns ErrNotFound.
func (e *EAT) Update(bucketId string, newRoot, parentRoot cid.Cid, arcs map[string]cid.Cid) error {
	ctx := context.Background()

	batch := e.kv.Batch()

	// Store all arcs for this version
	for path, target := range arcs {
		key := arcKey(bucketId, newRoot, path)
		if target == cid.Undef {
			// Store tombstone (empty bytes) to mark deletion
			if err := batch.Put(key, []byte{}); err != nil {
				batch.Cancel()
				return fmt.Errorf("failed to add tombstone for arc %s: %w", path, err)
			}
		} else {
			val := target.Bytes()
			if err := batch.Put(key, val); err != nil {
				batch.Cancel()
				return fmt.Errorf("failed to add arc %s to batch: %w", path, err)
			}
		}
	}

	// Link to parent via @previous (unless this is the first version)
	if parentRoot != cid.Undef {
		prevKey := arcKey(bucketId, newRoot, PreviousArc)
		prevVal := parentRoot.Bytes()
		if err := batch.Put(prevKey, prevVal); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to add @previous to batch: %w", err)
		}
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit version: %w", err)
	}

	return nil
}

// GetParent returns the parent version of a given version via @previous.
// Returns cid.Undef if the version has no parent (first version).
func (e *EAT) GetParent(bucketId string, version cid.Cid) (cid.Cid, error) {
	ctx := context.Background()

	prevKey := arcKey(bucketId, version, PreviousArc)
	prevVal, err := e.kv.Get(ctx, prevKey)
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Undef, nil // No parent
		}
		return cid.Cid{}, fmt.Errorf("failed to get @previous: %w", err)
	}

	return cid.Cast(prevVal)
}

// Snapshot returns an immutable snapshot of all arcs visible at the given version.
// This includes all arcs from the version and its ancestors via @previous chain.
func (e *EAT) Snapshot(bucketId string, version cid.Cid) arcset.Snapshot {
	arcs := e.collectFlattenedArcs(bucketId, version)
	return arcset.NewMapFrom(arcs)
}

// collectFlattenedArcs collects all arcs visible at a version (including ancestors).
func (e *EAT) collectFlattenedArcs(bucketId string, version cid.Cid) map[string]cid.Cid {
	ctx := context.Background()

	arcs := make(map[string]cid.Cid)
	seen := make(map[string]bool)
	currentVersion := version
	maxDepth := 1000

	for i := 0; i < maxDepth; i++ {
		prefix := versionPrefix(bucketId, currentVersion)
		iter := e.kv.NewIterator(ctx, prefix, nil)

		for iter.Next() {
			key := iter.Key()
			path := string(key[len(prefix):])

			if path == PreviousArc {
				continue
			}

			if seen[path] {
				continue
			}

			val := iter.Value()
			// Check for tombstone
			if len(val) == 0 {
				seen[path] = true // Mark as deleted
				continue
			}

			if c, err := cid.Cast(val); err == nil {
				arcs[path] = c
				seen[path] = true
			}
		}
		iter.Close()

		// Get parent version
		prevKey := arcKey(bucketId, currentVersion, PreviousArc)
		prevVal, err := e.kv.Get(ctx, prevKey)
		if err != nil {
			break
		}

		parentVersion, err := cid.Cast(prevVal)
		if err != nil {
			break
		}
		currentVersion = parentVersion
	}

	return arcs
}

// Iterate returns a streaming iterator over all arcs visible at the given version.
// This walks the @previous chain to yield arcs from the version and its ancestors.
// Caller must call Close() on the iterator when done.
func (e *EAT) Iterate(bucketId string, version cid.Cid) arcset.Iterator {
	return &chainIterator{
		eat:            e,
		bucketId:       bucketId,
		currentVersion: version,
		seen:           make(map[string]bool),
		maxDepth:       1000,
	}
}

// Close releases resources.
func (e *EAT) Close() error {
	return nil
}

// chainIterator walks the @previous chain to iterate all visible arcs.
type chainIterator struct {
	eat            *EAT
	bucketId       string
	currentVersion cid.Cid
	seen           map[string]bool
	maxDepth       int

	// Current batch of arcs being yielded
	currentBatch map[string]cid.Cid
	currentKeys  []string
	keyIndex     int

	// Error state
	err error
}

func (it *chainIterator) Next() (string, cid.Cid, bool) {
	// Try to yield from current batch
	for it.keyIndex < len(it.currentKeys) {
		path := it.currentKeys[it.keyIndex]
		it.keyIndex++

		if it.seen[path] {
			continue
		}
		it.seen[path] = true
		return path, it.currentBatch[path], true
	}

	// Need to load next version
	if it.currentVersion == cid.Undef || it.maxDepth <= 0 {
		return "", cid.Cid{}, false
	}

	it.maxDepth--

	// Load arcs from current version
	ctx := context.Background()
	prefix := versionPrefix(it.bucketId, it.currentVersion)
	iter := it.eat.kv.NewIterator(ctx, prefix, nil)

	it.currentBatch = make(map[string]cid.Cid)
	var nextVersion cid.Cid

	for iter.Next() {
		key := iter.Key()
		path := string(key[len(prefix):])

		if path == PreviousArc {
			val := iter.Value()
			if c, err := cid.Cast(val); err == nil {
				nextVersion = c
			}
			continue
		}

		if it.seen[path] {
			continue
		}

		val := iter.Value()
		// Handle tombstone
		if len(val) == 0 {
			it.seen[path] = true // Mark as deleted
			continue
		}

		if c, err := cid.Cast(val); err == nil {
			it.currentBatch[path] = c
		}
	}
	iter.Close()

	// Prepare keys for iteration
	it.currentKeys = make([]string, 0, len(it.currentBatch))
	for p := range it.currentBatch {
		it.currentKeys = append(it.currentKeys, p)
	}
	it.keyIndex = 0

	// Move to parent version
	it.currentVersion = nextVersion

	// Try again with new batch
	return it.Next()
}

func (it *chainIterator) Err() error {
	return it.err
}

func (it *chainIterator) Close() {
	// No persistent resources to release
	// Each iteration creates and closes its own kvstore.Iterator
}