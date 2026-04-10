// Package overwrite provides an EAT implementation with overwrite semantics.
// This EAT stores arc sets for single-root graphs with bucket-based isolation.
//
// Bloom Filter: Each bucket has a persistent bloom filter stored separately.
// The bloom filter is cached in memory for fast negative lookups.
//
// Concurrency Control: This implementation uses mutex for bloom filter synchronization.
// For production deployments, consider using a distributed KVStore backend
// (e.g., TiKV, CockroachDB) that provides transactional guarantees.
package overwrite

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// Default bloom filter parameters.
const (
	DefaultExpectedItems     = 10000
	DefaultFalsePositiveRate = 0.01
	DefaultCacheSize         = 100
)

// EAT is an EAT with overwrite semantics and bucket-based bloom filters.
// It uses bucketId for namespace isolation, allowing multiple graphs
// to share the same KVStore instance.
type EAT struct {
	kv kvstore.KVStore

	// Bloom filter configuration per bucket
	bucketConfigs sync.Map // bucketId -> *bloom.BucketConfig

	// Bloom filter cache
	bloomCache *bloom.Cache

	// Default bloom config for new buckets
	defaultConfig *bloom.BucketConfig
}

// NewEAT creates a new EAT with the given KVStore and default bloom parameters.
func NewEAT(kv kvstore.KVStore) (*EAT, error) {
	return NewEATWithConfig(kv, bloom.DefaultBucketConfig(), DefaultCacheSize)
}

// NewEATWithConfig creates a new EAT with custom bloom configuration.
func NewEATWithConfig(kv kvstore.KVStore, defaultConfig *bloom.BucketConfig, cacheSize int) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	if defaultConfig == nil {
		defaultConfig = bloom.DefaultBucketConfig()
	}
	if cacheSize <= 0 {
		cacheSize = DefaultCacheSize
	}

	return &EAT{
		kv:            kv,
		bloomCache:    bloom.NewCache(cacheSize),
		defaultConfig: defaultConfig,
	}, nil
}

// NewEATWithBloomParams creates a new EAT with custom bloom filter parameters.
// Deprecated: Use NewEATWithConfig instead.
func NewEATWithBloomParams(kv kvstore.KVStore, expectedItems int, falsePositiveRate float64) (*EAT, error) {
	return NewEATWithConfig(kv, &bloom.BucketConfig{
		ExpectedItems:     expectedItems,
		FalsePositiveRate: falsePositiveRate,
	}, DefaultCacheSize)
}

// NewEATWithoutBloom creates a new EAT without bloom filter optimization.
func NewEATWithoutBloom(kv kvstore.KVStore) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{
		kv:            kv,
		bloomCache:    nil, // disabled
		defaultConfig: nil,
	}, nil
}

// arcKey generates the storage key for a path within a bucket.
// Format: bucketId:path
func arcKey(bucketId, path string) []byte {
	return []byte(bucketId + ":" + path)
}

// bucketPrefix generates the prefix for all arcs in a bucket.
// Format: bucketId:
func bucketPrefix(bucketId string) []byte {
	return []byte(bucketId + ":")
}

// rootKey generates the key for root->bucketId mapping.
// Format: root:{cid}
func rootKey(root cid.Cid) []byte {
	return []byte("root:" + root.String())
}

// bucketBloomKey generates the key for a bucket's bloom filter.
// Format: bloom:bucketId
func bucketBloomKey(bucketId string) []byte {
	return []byte("bloom:" + bucketId)
}

// CreateBucket creates a new bucket with custom bloom configuration.
func (e *EAT) CreateBucket(ctx context.Context, bucketId string, cfg *bloom.BucketConfig) error {
	if cfg == nil {
		cfg = e.defaultConfig
	}

	e.bucketConfigs.Store(bucketId, cfg)

	// Initialize empty bloom filter
	filter := bloom.NewStandardBloom(cfg.ExpectedItems, cfg.FalsePositiveRate)
	return e.saveBucketBloom(ctx, bucketId, filter)
}

// GetBucketConfig returns the bloom configuration for a bucket.
func (e *EAT) GetBucketConfig(bucketId string) *bloom.BucketConfig {
	if val, ok := e.bucketConfigs.Load(bucketId); ok {
		return val.(*bloom.BucketConfig)
	}
	return e.defaultConfig
}

// getBucketBloom loads or creates a bloom filter for a bucket.
func (e *EAT) getBucketBloom(ctx context.Context, bucketId string) (*bloom.StandardBloom, error) {
	if e.bloomCache == nil {
		return nil, nil // Bloom disabled
	}

	// Check cache first
	if cached := e.bloomCache.Get(bucketId); cached != nil {
		return cached.(*bloom.StandardBloom), nil
	}

	// Load from KVStore
	key := bucketBloomKey(bucketId)
	data, err := e.kv.Get(ctx, key)
	if err == kvstore.ErrNotFound {
		// Create new bloom filter
		cfg := e.GetBucketConfig(bucketId)
		filter := bloom.NewStandardBloom(cfg.ExpectedItems, cfg.FalsePositiveRate)
		e.bloomCache.Set(bucketId, filter)
		return filter, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load bloom filter: %w", err)
	}

	// Deserialize
	filter := &bloom.StandardBloom{}
	if err := filter.UnmarshalBinary(data); err != nil {
		return nil, fmt.Errorf("failed to deserialize bloom filter: %w", err)
	}

	// Cache it
	e.bloomCache.Set(bucketId, filter)

	return filter, nil
}

// saveBucketBloom persists a bloom filter to KVStore and cache.
func (e *EAT) saveBucketBloom(ctx context.Context, bucketId string, filter *bloom.StandardBloom) error {
	if e.bloomCache == nil {
		return nil // Bloom disabled
	}

	// Serialize
	data, err := filter.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to serialize bloom filter: %w", err)
	}

	// Persist to KVStore
	key := bucketBloomKey(bucketId)
	if err := e.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("failed to persist bloom filter: %w", err)
	}

	// Update cache
	e.bloomCache.Set(bucketId, filter)

	return nil
}

// MightContain checks if a path might exist in the bucket using bloom filter.
// Returns false if the path definitely doesn't exist (can skip KVStore lookup).
// Returns true if the path might exist (need to call Get to verify).
func (e *EAT) MightContain(ctx context.Context, bucketId string, path string) bool {
	if e.bloomCache == nil {
		return true // Bloom disabled, conservatively return true
	}

	filter, err := e.getBucketBloom(ctx, bucketId)
	if err != nil || filter == nil {
		return true // No bloom for this bucket, conservatively return true
	}

	return filter.Test([]byte(path))
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

	filter, err := e.getBucketBloom(ctx, bucketId)
	if err != nil || filter == nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	for _, p := range paths {
		result[p] = filter.Test([]byte(p))
	}

	return result
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
		rootKeyBytes := rootKey(root)
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
	arcKeyBytes := arcKey(bucketId, path)
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
		rootKeyBytes := rootKey(root)
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
		key := arcKey(bucketId, path)
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
		oldRootKey := rootKey(oldRoot)
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
		newRootKey := rootKey(newRoot)
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
		key := arcKey(bucketId, path)
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
		if err := e.updateBucketBloom(ctx, bucketId, addedPaths); err != nil {
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

// updateBucketBloom updates the bucket bloom filter with new paths.
func (e *EAT) updateBucketBloom(ctx context.Context, bucketId string, addedPaths []string) error {
	// Get or create bloom filter
	filter, err := e.getBucketBloom(ctx, bucketId)
	if err != nil {
		return err
	}
	if filter == nil {
		return nil
	}

	// Add new paths to bloom
	for _, path := range addedPaths {
		filter.Add([]byte(path))
	}

	// Persist updated bloom
	return e.saveBucketBloom(ctx, bucketId, filter)
}

// Snapshot returns an immutable snapshot of all arcs in the bucket.
func (e *EAT) Snapshot(ctx context.Context, bucketId string, root cid.Cid) (arcset.Snapshot, error) {
	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := rootKey(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(bucketIdBytes) != bucketId {
			return arcset.NewMap(), nil // Return empty snapshot for invalid root
		}
	}

	// Load all arcs into memory
	arcs := make(map[string]cid.Cid)
	prefix := bucketPrefix(bucketId)
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
		rootKeyBytes := rootKey(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(bucketIdBytes) != bucketId {
			return &emptyIterator{}
		}
	}

	prefix := bucketPrefix(bucketId)
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