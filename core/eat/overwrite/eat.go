// Package overwrite provides an EAT implementation with overwrite semantics.
// This EAT stores arc sets for single-root graphs with bucket-based isolation.
//
// Bloom Filter: Uses BloomCache component for fast negative lookups.
//
// Concurrency Control: This implementation uses mutex for bloom filter synchronization.
// For production deployments, consider using a distributed KVStore backend
// (e.g., TiKV, CockroachDB) that provides transactional guarantees.
package overwrite

import (
	"context"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// EAT is an EAT with overwrite semantics.
// It uses bucketId for namespace isolation, allowing multiple graphs
// to share the same KVStore instance.
type EAT struct {
	kv         kvstore.KVStore
	bloomCache *bloom.BloomCache // Optional, can be nil
}

// NewEAT creates a new EAT with the given KVStore and optional configuration.
// Bloom filter is disabled by default; use WithBloomCache to enable it.
//
// Example usage:
//
//	// Simple: use defaults
//	eat, _ := overwrite.NewEAT(overwrite.WithKVStore(kv))
//
//	// With bloom cache
//	eat, _ := overwrite.NewEAT(
//	    overwrite.WithKVStore(kv),
//	    overwrite.WithBloomCache(bloomCache),
//	)
func NewEAT(opts ...Option) (*EAT, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	if o.kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}
	return &EAT{
		kv:         o.kv,
		bloomCache: o.bloomCache,
	}, nil
}

// NewEATWithBloomCache creates a new EAT with BloomCache for fast negative lookups.
// Deprecated: Use NewEAT with WithBloomCache option instead.
func NewEATWithBloomCache(kv kvstore.KVStore, bloomCache *bloom.BloomCache) (*EAT, error) {
	return NewEAT(WithKVStore(kv), WithBloomCache(bloomCache))
}

// CreateBucket creates a new bucket with custom bloom configuration.
func (e *EAT) CreateBucket(ctx context.Context, bucketId string, cfg *bloom.BucketConfig) error {
	if e.bloomCache == nil {
		return fmt.Errorf("bloom cache not configured")
	}
	return e.bloomCache.CreateBucket(ctx, bucketId, cfg)
}

// MightContain checks if a path might exist in the bucket using bloom filter.
// Returns false if the path definitely doesn't exist (can skip KVStore lookup).
// Returns true if the path might exist (need to call Get to verify).
func (e *EAT) MightContain(ctx context.Context, bucketId string, path string) bool {
	if e.bloomCache == nil {
		return true // Bloom disabled
	}
	result, err := e.bloomCache.MightContain(ctx, bucketId, path)
	if err != nil {
		return true // On error, conservatively return true
	}
	return result
}

// MightContainBatch checks multiple paths at once using bloom filter.
func (e *EAT) MightContainBatch(ctx context.Context, bucketId string, paths []string) map[string]bool {
	result := make(map[string]bool, len(paths))

	if e.bloomCache == nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	batchResult, err := e.bloomCache.MightContainBatch(ctx, bucketId, paths)
	if err != nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	return batchResult
}

// Get retrieves the target CID for a path within a bucket.
// First checks bloom filter, then queries KVStore.
func (e *EAT) Get(ctx context.Context, bucketId string, root cid.Cid, path string) (cid.Cid, error) {
	start := time.Now()

	logger.Debug("EAT.Get started",
		logger.String("bucket", bucketId),
		logger.String("root", root.String()),
		logger.String("path", path))

	// Quick bloom filter check
	if !e.MightContain(ctx, bucketId, path) {
		logger.Debug("EAT.Get bloom negative",
			logger.String("bucket", bucketId),
			logger.String("path", path))
		return cid.Cid{}, arcset.ErrNotFound
	}

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := eat.RootKeyFormat(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil {
			if err == kvstore.ErrNotFound {
				logger.Debug("EAT.Get root not found",
					logger.String("bucket", bucketId),
					logger.String("root", root.String()))
				return cid.Cid{}, arcset.ErrNotFound
			}
			logger.Error("EAT.Get failed to resolve root",
				logger.String("bucket", bucketId),
				logger.String("root", root.String()),
				logger.Err(err))
			return cid.Cid{}, fmt.Errorf("failed to resolve root: %w", err)
		}

		// Verify this root maps to the correct bucket
		if string(bucketIdBytes) != bucketId {
			logger.Debug("EAT.Get root bucket mismatch",
				logger.String("bucket", bucketId),
				logger.String("root", root.String()),
				logger.String("expected_bucket", string(bucketIdBytes)))
			return cid.Cid{}, arcset.ErrNotFound
		}
	}

	// Get the arc
	arcKeyBytes := eat.DefaultArcKey(bucketId, path)
	val, err := e.kv.Get(ctx, arcKeyBytes)
	if err != nil {
		if err == kvstore.ErrNotFound {
			logger.Debug("EAT.Get arc not found",
				logger.String("bucket", bucketId),
				logger.String("path", path))
			return cid.Cid{}, arcset.ErrNotFound
		}
		logger.Error("EAT.Get failed to get arc",
			logger.String("bucket", bucketId),
			logger.String("path", path),
			logger.Err(err))
		return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
	}

	result, err := cid.Cast(val)
	if err == nil {
		logger.Debug("EAT.Get success",
			logger.String("bucket", bucketId),
			logger.String("path", path),
			logger.String("target", result.String()),
			logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))
	}
	return result, err
}

// BatchGet retrieves multiple target CIDs in a single operation.
// Uses bloom filter to filter out definitely-not-present paths.
func (e *EAT) BatchGet(ctx context.Context, bucketId string, root cid.Cid, paths []string) (map[string]cid.Cid, error) {
	start := time.Now()

	logger.Debug("EAT.BatchGet started",
		logger.String("bucket", bucketId),
		logger.String("root", root.String()),
		logger.Int("path_count", len(paths)))

	// Filter paths using bloom filter
	mightExist := e.MightContainBatch(ctx, bucketId, paths)
	filteredPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		if mightExist[p] {
			filteredPaths = append(filteredPaths, p)
		}
	}

	if len(filteredPaths) == 0 {
		logger.Debug("EAT.BatchGet all filtered by bloom",
			logger.String("bucket", bucketId))
		return map[string]cid.Cid{}, nil
	}

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := eat.RootKeyFormat(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil {
			if err == kvstore.ErrNotFound {
				logger.Debug("EAT.BatchGet root not found",
					logger.String("bucket", bucketId),
					logger.String("root", root.String()))
				return nil, arcset.ErrNotFound
			}
			logger.Error("EAT.BatchGet failed to resolve root",
				logger.String("bucket", bucketId),
				logger.Err(err))
			return nil, fmt.Errorf("failed to resolve root: %w", err)
		}

		// Verify this root maps to the correct bucket
		if string(bucketIdBytes) != bucketId {
			logger.Debug("EAT.BatchGet root bucket mismatch",
				logger.String("bucket", bucketId),
				logger.String("expected_bucket", string(bucketIdBytes)))
			return nil, arcset.ErrNotFound
		}
	}

	// Build keys for batch get
	keys := make([][]byte, len(filteredPaths))
	pathToKey := make(map[string]string, len(filteredPaths))
	for i, path := range filteredPaths {
		key := eat.DefaultArcKey(bucketId, path)
		keys[i] = key
		pathToKey[string(key)] = path
	}

	// Use KVStore BatchGet for efficient bulk retrieval
	kvResults, err := e.kv.BatchGet(ctx, keys)
	if err != nil {
		logger.Error("EAT.BatchGet kv error",
			logger.String("bucket", bucketId),
			logger.Err(err))
		return nil, fmt.Errorf("failed to batch get arcs: %w", err)
	}

	// Convert results to CID map
	results := make(map[string]cid.Cid)
	for keyStr, val := range kvResults {
		path := pathToKey[keyStr]
		if c, err := cid.Cast(val); err == nil {
			results[path] = c
		}
	}

	logger.Debug("EAT.BatchGet completed",
		logger.String("bucket", bucketId),
		logger.Int("requested_count", len(paths)),
		logger.Int("filtered_count", len(filteredPaths)),
		logger.Int("found_count", len(results)),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return results, nil
}

// Update stores arc entries with a new commitment root.
// Updates the bucket bloom filter incrementally.
func (e *EAT) Update(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid, arcs map[string]cid.Cid) error {
	start := time.Now()

	logger.Info("EAT.Update started",
		logger.String("bucket", bucketId),
		logger.String("new_root", newRoot.String()),
		logger.String("old_root", oldRoot.String()),
		logger.Int("arc_count", len(arcs)))

	batch := e.kv.Batch()

	// Remove old root mapping if exists
	if oldRoot != cid.Undef {
		oldRootKey := eat.RootKeyFormat(oldRoot)
		if err := batch.Delete(oldRootKey); err != nil {
			batch.Cancel()
			logger.Error("EAT.Update failed to delete old root",
				logger.String("bucket", bucketId),
				logger.String("old_root", oldRoot.String()),
				logger.Err(err))
			return fmt.Errorf("failed to delete old root mapping: %w", err)
		}
	}

	// Add new root mapping if provided
	if newRoot != cid.Undef {
		newRootKey := eat.RootKeyFormat(newRoot)
		if err := batch.Put(newRootKey, []byte(bucketId)); err != nil {
			batch.Cancel()
			logger.Error("EAT.Update failed to add new root",
				logger.String("bucket", bucketId),
				logger.String("new_root", newRoot.String()),
				logger.Err(err))
			return fmt.Errorf("failed to add new root mapping: %w", err)
		}
	}

	// Track added paths for bloom filter update
	var addedPaths []string

	// Add/Update/Delete arcs
	for path, target := range arcs {
		key := eat.DefaultArcKey(bucketId, path)
		if target == cid.Undef {
			// Delete the arc
			if err := batch.Delete(key); err != nil {
				batch.Cancel()
				logger.Error("EAT.Update failed to delete arc",
					logger.String("bucket", bucketId),
					logger.String("path", path),
					logger.Err(err))
				return fmt.Errorf("failed to delete arc %s: %w", path, err)
			}
		} else {
			addedPaths = append(addedPaths, path)
			// Add/Update the arc
			val := target.Bytes()
			if err := batch.Put(key, val); err != nil {
				batch.Cancel()
				logger.Error("EAT.Update failed to put arc",
					logger.String("bucket", bucketId),
					logger.String("path", path),
					logger.Err(err))
				return fmt.Errorf("failed to put arc %s: %w", path, err)
			}
		}
	}

	if err := batch.Commit(ctx); err != nil {
		logger.Error("EAT.Update commit failed",
			logger.String("bucket", bucketId),
			logger.Err(err))
		return fmt.Errorf("failed to commit update: %w", err)
	}

	// Update bucket bloom filter after successful commit
	if e.bloomCache != nil && len(addedPaths) > 0 {
		if err := e.bloomCache.Add(ctx, bucketId, addedPaths); err != nil {
			logger.Warn("EAT.Update failed to update bloom (non-fatal)",
				logger.String("bucket", bucketId),
				logger.Err(err))
			// Non-fatal: bloom is optional optimization
		}
	}

	logger.Info("EAT.Update completed",
		logger.String("bucket", bucketId),
		logger.String("new_root", newRoot.String()),
		logger.Int("arc_count", len(arcs)),
		logger.Int("added_count", len(addedPaths)),
		logger.Bool("invalidated_old_root", oldRoot != cid.Undef),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return nil
}

// Snapshot returns an immutable snapshot of all arcs in the bucket.
func (e *EAT) Snapshot(ctx context.Context, bucketId string, root cid.Cid) (arcset.Snapshot, error) {
	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := eat.RootKeyFormat(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(bucketIdBytes) != bucketId {
			return arcset.NewMap(), nil // Return empty snapshot for invalid root
		}
	}

	// Load all arcs into memory
	arcs := make(map[string]cid.Cid)
	prefix := eat.DefaultBucketPrefix(bucketId)
	iter := e.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	for iter.Next() {
		key := iter.Key()
		path := string(key[len(prefix):])
		val := iter.Value()
		if c, err := cid.Cast(val); err == nil {
			arcs[path] = c
		}
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	return arcset.NewMapFrom(arcs), nil
}

// Iterate returns a streaming iterator over all arcs in the bucket.
func (e *EAT) Iterate(ctx context.Context, bucketId string, root cid.Cid) arcset.Iterator {
	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := eat.RootKeyFormat(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(bucketIdBytes) != bucketId {
			return &emptyIterator{}
		}
	}

	prefix := eat.DefaultBucketPrefix(bucketId)
	iter := e.kv.NewIterator(ctx, prefix, nil)

	return &eatIterator{
		iter:   iter,
		prefix: prefix,
	}
}

// Close releases resources.
func (e *EAT) Close() error {
	if e.bloomCache != nil {
		e.bloomCache.Clear()
	}
	return nil
}

// Stats returns bloom filter cache statistics.
func (e *EAT) Stats() map[string]interface{} {
	if e.bloomCache == nil {
		return map[string]interface{}{
			"bloom_enabled": false,
		}
	}
	return map[string]interface{}{
		"bloom_enabled": true,
		"cache_size":    e.bloomCache.Size(),
	}
}

// eatIterator implements arcset.Iterator for the EAT.
type eatIterator struct {
	iter   kvstore.Iterator
	prefix []byte
}

func (it *eatIterator) Next() (string, cid.Cid, bool) {
	for {
		if !it.iter.Next() {
			return "", cid.Cid{}, false
		}

		key := it.iter.Key()
		path := string(key[len(it.prefix):])

		val := it.iter.Value()
		c, err := cid.Cast(val)
		if err != nil {
			continue // Skip invalid entries
		}

		return path, c, true
	}
}

func (it *eatIterator) Err() error {
	return it.iter.Err()
}

func (it *eatIterator) Close() {
	it.iter.Close()
}

// emptyIterator is an empty iterator for invalid root cases.
type emptyIterator struct{}

func (it *emptyIterator) Next() (string, cid.Cid, bool) {
	return "", cid.Cid{}, false
}

func (it *emptyIterator) Err() error {
	return nil
}

func (it *emptyIterator) Close() {}
