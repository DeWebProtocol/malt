// Package overwrite provides an EAT implementation with overwrite semantics.
// This EAT stores arc sets for single-root graphs with bucket-based isolation.
//
// Bloom Filter: Each bucket has an internal bloom filter for fast negative lookups.
// The bloom filter is updated incrementally on each Update operation.
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
)

// EAT is an EAT with overwrite semantics and internal bloom filters.
// It uses bucketId for namespace isolation, allowing multiple graphs
// to share the same KVStore instance.
type EAT struct {
	kv kvstore.KVStore

	// Bloom filters per bucket
	blooms     sync.Map // bucketId -> *bucketBloom
	newBloom   func() bloom.BloomFilter
}

// bucketBloom holds a bloom filter with its metadata.
type bucketBloom struct {
	filter bloom.BloomFilter
	mu     sync.RWMutex
}

// NewEAT creates a new EAT with the given KVStore and default bloom parameters.
func NewEAT(kv kvstore.KVStore) (*EAT, error) {
	return NewEATWithBloomParams(kv, DefaultExpectedItems, DefaultFalsePositiveRate)
}

// NewEATWithBloomParams creates a new EAT with custom bloom filter parameters.
func NewEATWithBloomParams(kv kvstore.KVStore, expectedItems int, falsePositiveRate float64) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{
		kv: kv,
		newBloom: func() bloom.BloomFilter {
			return bloom.NewStandardBloom(expectedItems, falsePositiveRate)
		},
	}, nil
}

// NewEATWithoutBloom creates a new EAT without bloom filter optimization.
func NewEATWithoutBloom(kv kvstore.KVStore) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{
		kv:      kv,
		newBloom: nil, // disabled
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

// MightContain checks if a path might exist in the bucket using bloom filter.
// Returns false if the path definitely doesn't exist (can skip KVStore lookup).
// Returns true if the path might exist (need to call Get to verify).
func (e *EAT) MightContain(bucketId string, path string) bool {
	if e.newBloom == nil {
		return true // Bloom disabled, conservatively return true
	}

	bb := e.getBucketBloom(bucketId)
	if bb == nil {
		return true // No bloom for this bucket, conservatively return true
	}

	bb.mu.RLock()
	result := bb.filter.Test([]byte(path))
	bb.mu.RUnlock()

	return result
}

// MightContainBatch checks multiple paths at once using bloom filter.
func (e *EAT) MightContainBatch(bucketId string, paths []string) map[string]bool {
	result := make(map[string]bool, len(paths))

	if e.newBloom == nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	bb := e.getBucketBloom(bucketId)
	if bb == nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	bb.mu.RLock()
	for _, p := range paths {
		result[p] = bb.filter.Test([]byte(p))
	}
	bb.mu.RUnlock()

	return result
}

// getBucketBloom gets the bloom filter for a bucket.
func (e *EAT) getBucketBloom(bucketId string) *bucketBloom {
	val, ok := e.blooms.Load(bucketId)
	if !ok {
		return nil
	}
	return val.(*bucketBloom)
}

// updateBloom updates the bloom filter for a bucket incrementally.
func (e *EAT) updateBloom(bucketId string, addedPaths, deletedPaths []string) {
	if e.newBloom == nil {
		return
	}

	bb := e.getBucketBloom(bucketId)
	if bb == nil {
		// Create new bloom filter
		bb = &bucketBloom{filter: e.newBloom()}
		e.blooms.Store(bucketId, bb)
	}

	bb.mu.Lock()
	defer bb.mu.Unlock()

	// Add new paths
	for _, path := range addedPaths {
		bb.filter.Add([]byte(path))
	}

	// Note: Bloom filters don't support deletion efficiently.
	// Deleted paths remain in the filter, which may increase false positives.
	// For production, consider using a Counting Bloom Filter or rebuilding periodically.
	if len(deletedPaths) > 0 {
		logger.Debug("EAT.updateBloom deletions noted",
			logger.String("bucket", bucketId),
			logger.Int("deleted_count", len(deletedPaths)))
	}
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
	if !e.MightContain(bucketId, path) {
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
	mightExist := e.MightContainBatch(bucketId, paths)
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
// Updates the bloom filter incrementally.
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

	// Track added and deleted paths for bloom filter update
	var addedPaths, deletedPaths []string

	// Add/Update/Delete arcs
	for path, target := range arcs {
		key := arcKey(bucketId, path)
		if target == cid.Undef {
			deletedPaths = append(deletedPaths, path)
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

	// Update bloom filter after successful commit
	e.updateBloom(bucketId, addedPaths, deletedPaths)

	logger.Info("EAT.Update completed",
		logger.String("bucket", bucketId),
		logger.String("new_root", newRoot.String()),
		logger.Int("arc_count", len(arcs)),
		logger.Int("added_count", len(addedPaths)),
		logger.Int("deleted_count", len(deletedPaths)),
		logger.Bool("invalidated_old_root", oldRoot != cid.Undef),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return nil
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
	// Clear all bloom filters
	e.blooms.Range(func(key, value interface{}) bool {
		e.blooms.Delete(key)
		return true
	})
	return nil
}

// Stats returns bloom filter statistics.
func (e *EAT) Stats() map[string]uint64 {
	stats := make(map[string]uint64)
	e.blooms.Range(func(key, value interface{}) bool {
		bucketId := key.(string)
		bb := value.(*bucketBloom)
		bb.mu.RLock()
		stats[bucketId] = bb.filter.Size()
		bb.mu.RUnlock()
		return true
	})
	return stats
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