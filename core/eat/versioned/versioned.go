// Package versioned provides a versioned EAT implementation using a KVStore.
// Each version stores only modified arcs plus a @previous arc pointing to the parent version.
// Resolution walks the @previous chain to find arc entries.
//
// Bloom Filter: Uses BloomCache component for fast negative lookups.
//
// Concurrency: This implementation is inherently concurrency-safe because each update
// creates a new version with its own namespace (bucketId:version:path).
package versioned

import (
	"context"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// Reserved arc paths
const (
	PreviousArc = "@previous" // Points to parent version's commitment root
)


// EAT is a versioned EAT implementation with bucket-based isolation.
// Each version stores only modified arcs, with @previous linking to the parent.
type EAT struct {
	kv         kvstore.KVStore
	bloomCache *bloom.BloomCache // Optional, can be nil
}

// NewEAT creates a new versioned EAT with the given KVStore and optional configuration.
// Bloom filter is disabled by default; use WithBloomCache to enable it.
//
// Example usage:
//
//	// Simple: use defaults
//	eat, _ := versioned.NewEAT(versioned.WithKVStore(kv))
//
//	// With bloom cache
//	eat, _ := versioned.NewEAT(
//	    versioned.WithKVStore(kv),
//	    versioned.WithBloomCache(bloomCache),
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

// NewEATWithBloomCache creates a new versioned EAT with BloomCache for fast negative lookups.
// Deprecated: Use NewEAT with WithBloomCache option instead.
func NewEATWithBloomCache(kv kvstore.KVStore, bloomCache *bloom.BloomCache) (*EAT, error) {
	return NewEAT(WithKVStore(kv), WithBloomCache(bloomCache))
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

// CreateBucket creates a new bucket with custom bloom configuration.
func (e *EAT) CreateBucket(ctx context.Context, bucketId string, cfg *bloom.BucketConfig) error {
	if e.bloomCache == nil {
		return fmt.Errorf("bloom cache not configured")
	}
	return e.bloomCache.CreateBucket(ctx, bucketId, cfg)
}

// MightContain checks if a path might exist in a bucket using bloom filter.
// Returns false if the path definitely doesn't exist.
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

// Get retrieves the target CID for a path at a specific version.
// First checks bloom filter, then walks the @previous chain.
func (e *EAT) Get(ctx context.Context, bucketId string, version cid.Cid, path string) (cid.Cid, error) {
	start := time.Now()

	// Quick bloom filter check
	if !e.MightContain(ctx, bucketId, path) {
		logger.Debug("EAT.Get bloom negative",
			logger.String("bucket", bucketId),
			logger.String("version", version.String()),
			logger.String("path", path))
		return cid.Cid{}, arcset.ErrNotFound
	}

	currentVersion := version
	maxDepth := 1000
	depth := 0

	logger.Debug("EAT.Get started",
		logger.String("bucket", bucketId),
		logger.String("version", version.String()),
		logger.String("path", path))

	for range maxDepth {
		depth++
		if ctx.Err() != nil {
			logger.Warn("EAT.Get cancelled",
				logger.String("bucket", bucketId),
				logger.String("path", path),
				logger.Int("depth", depth),
				logger.Err(ctx.Err()))
			return cid.Cid{}, ctx.Err()
		}

		// Try to get the arc at current version
		key := arcKey(bucketId, currentVersion, path)
		val, err := e.kv.Get(ctx, key)
		if err == nil {
			if len(val) == 0 {
				logger.Debug("EAT.Get found tombstone",
					logger.String("bucket", bucketId),
					logger.String("path", path),
					logger.Int("depth", depth))
				return cid.Cid{}, arcset.ErrNotFound
			}
			result, err := cid.Cast(val)
			if err == nil {
				logger.Debug("EAT.Get success",
					logger.String("bucket", bucketId),
					logger.String("path", path),
					logger.String("target", result.String()),
					logger.Int("depth", depth),
					logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))
			}
			return result, err
		}

		if err != kvstore.ErrNotFound {
			logger.Error("EAT.Get kv error",
				logger.String("bucket", bucketId),
				logger.String("path", path),
				logger.Err(err))
			return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
		}

		// Arc not found at this version, try parent
		prevKey := arcKey(bucketId, currentVersion, PreviousArc)
		prevVal, err := e.kv.Get(ctx, prevKey)
		if err != nil {
			if err == kvstore.ErrNotFound {
				logger.Debug("EAT.Get not found (no parent)",
					logger.String("bucket", bucketId),
					logger.String("path", path),
					logger.Int("depth", depth))
				return cid.Cid{}, arcset.ErrNotFound
			}
			return cid.Cid{}, fmt.Errorf("failed to get @previous: %w", err)
		}

		parentVersion, err := cid.Cast(prevVal)
		if err != nil {
			logger.Error("EAT.Get invalid @previous CID",
				logger.String("bucket", bucketId),
				logger.String("path", path),
				logger.Err(err))
			return cid.Cid{}, fmt.Errorf("invalid @previous CID: %w", err)
		}
		currentVersion = parentVersion
	}

	logger.Warn("EAT.Get exceeded max depth",
		logger.String("bucket", bucketId),
		logger.String("path", path),
		logger.Int("maxDepth", maxDepth))
	return cid.Cid{}, fmt.Errorf("version chain too deep (max %d)", maxDepth)
}

// BatchGet retrieves multiple target CIDs in a single operation.
func (e *EAT) BatchGet(ctx context.Context, bucketId string, version cid.Cid, paths []string) (map[string]cid.Cid, error) {
	start := time.Now()

	// Filter paths using bloom filter
	mightExist := e.MightContainBatch(ctx, bucketId, paths)
	filteredPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		if mightExist[p] {
			filteredPaths = append(filteredPaths, p)
		}
	}

	if len(filteredPaths) == 0 {
		return map[string]cid.Cid{}, nil
	}

	logger.Debug("EAT.BatchGet started",
		logger.String("bucket", bucketId),
		logger.String("version", version.String()),
		logger.Int("path_count", len(paths)),
		logger.Int("filtered_count", len(filteredPaths)))

	remaining := make(map[string]bool)
	for _, path := range filteredPaths {
		remaining[path] = true
	}

	results := make(map[string]cid.Cid)
	tombstones := make(map[string]bool)

	currentVersion := version
	maxDepth := 1000
	depth := 0

	for len(remaining) > 0 && maxDepth > 0 {
		maxDepth--
		depth++

		if ctx.Err() != nil {
			logger.Warn("EAT.BatchGet cancelled",
				logger.String("bucket", bucketId),
				logger.Int("depth", depth),
				logger.Err(ctx.Err()))
			return nil, ctx.Err()
		}

		keys := make([][]byte, 0, len(remaining))
		pathForKey := make(map[string]string)
		for path := range remaining {
			if tombstones[path] {
				continue
			}
			key := arcKey(bucketId, currentVersion, path)
			keys = append(keys, key)
			pathForKey[string(key)] = path
		}

		if len(keys) == 0 {
			break
		}

		kvResults, err := e.kv.BatchGet(ctx, keys)
		if err != nil {
			logger.Error("EAT.BatchGet kv error",
				logger.String("bucket", bucketId),
				logger.Int("depth", depth),
				logger.Err(err))
			return nil, fmt.Errorf("failed to batch get arcs: %w", err)
		}

		for keyStr, val := range kvResults {
			path := pathForKey[keyStr]
			if len(val) == 0 {
				tombstones[path] = true
			} else if c, err := cid.Cast(val); err == nil {
				results[path] = c
			}
			delete(remaining, path)
		}

		for path := range tombstones {
			delete(remaining, path)
		}

		if len(remaining) == 0 {
			break
		}

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

	logger.Debug("EAT.BatchGet completed",
		logger.String("bucket", bucketId),
		logger.Int("found_count", len(results)),
		logger.Int("depth", depth),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return results, nil
}

// Update stores arcs at a new version and updates the bucket bloom filter.
func (e *EAT) Update(ctx context.Context, bucketId string, newRoot, parentRoot cid.Cid, arcs map[string]cid.Cid) error {
	start := time.Now()

	logger.Info("EAT.Update started",
		logger.String("bucket", bucketId),
		logger.String("new_root", newRoot.String()),
		logger.String("parent_root", parentRoot.String()),
		logger.Int("arc_count", len(arcs)))

	batch := e.kv.Batch()

	tombstoneCount := 0
	addedPaths := make([]string, 0, len(arcs))

	// Store all arcs for this version
	for path, target := range arcs {
		key := arcKey(bucketId, newRoot, path)
		if target == cid.Undef {
			tombstoneCount++
			if err := batch.Put(key, []byte{}); err != nil {
				batch.Cancel()
				logger.Error("EAT.Update failed to add tombstone",
					logger.String("bucket", bucketId),
					logger.String("path", path),
					logger.Err(err))
				return fmt.Errorf("failed to add tombstone for arc %s: %w", path, err)
			}
		} else {
			val := target.Bytes()
			if err := batch.Put(key, val); err != nil {
				batch.Cancel()
				logger.Error("EAT.Update failed to add arc",
					logger.String("bucket", bucketId),
					logger.String("path", path),
					logger.Err(err))
				return fmt.Errorf("failed to add arc %s to batch: %w", path, err)
			}
			addedPaths = append(addedPaths, path)
		}
	}

	// Link to parent via @previous
	if parentRoot != cid.Undef {
		prevKey := arcKey(bucketId, newRoot, PreviousArc)
		prevVal := parentRoot.Bytes()
		if err := batch.Put(prevKey, prevVal); err != nil {
			batch.Cancel()
			logger.Error("EAT.Update failed to add @previous",
				logger.String("bucket", bucketId),
				logger.Err(err))
			return fmt.Errorf("failed to add @previous to batch: %w", err)
		}
	}

	// Commit the batch
	if err := batch.Commit(ctx); err != nil {
		logger.Error("EAT.Update commit failed",
			logger.String("bucket", bucketId),
			logger.Err(err))
		return fmt.Errorf("failed to commit version: %w", err)
	}

	// Update bucket bloom filter
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
		logger.Int("tombstone_count", tombstoneCount),
		logger.Bool("has_parent", parentRoot != cid.Undef),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return nil
}

// GetParent returns the parent version of a given version via @previous.
func (e *EAT) GetParent(ctx context.Context, bucketId string, version cid.Cid) (cid.Cid, error) {
	prevKey := arcKey(bucketId, version, PreviousArc)
	prevVal, err := e.kv.Get(ctx, prevKey)
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Undef, nil
		}
		return cid.Cid{}, fmt.Errorf("failed to get @previous: %w", err)
	}

	return cid.Cast(prevVal)
}

// Snapshot returns an immutable snapshot of all arcs visible at the given version.
func (e *EAT) Snapshot(ctx context.Context, bucketId string, version cid.Cid) (arcset.Snapshot, error) {
	start := time.Now()

	logger.Debug("EAT.Snapshot started",
		logger.String("bucket", bucketId),
		logger.String("version", version.String()))

	arcs, err := e.collectFlattenedArcs(ctx, bucketId, version)
	if err != nil {
		logger.Error("EAT.Snapshot failed",
			logger.String("bucket", bucketId),
			logger.String("version", version.String()),
			logger.Err(err))
		return nil, err
	}

	logger.Debug("EAT.Snapshot completed",
		logger.String("bucket", bucketId),
		logger.String("version", version.String()),
		logger.Int("arc_count", len(arcs)),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return arcset.NewMapFrom(arcs), nil
}

// collectFlattenedArcs collects all arcs visible at a version (including ancestors).
func (e *EAT) collectFlattenedArcs(ctx context.Context, bucketId string, version cid.Cid) (map[string]cid.Cid, error) {
	arcs := make(map[string]cid.Cid)
	seen := make(map[string]bool)
	currentVersion := version
	maxDepth := 1000

	for i := 0; i < maxDepth; i++ {
		if ctx.Err() != nil {
			logger.Warn("EAT.collectFlattenedArcs cancelled",
				logger.String("bucket", bucketId),
				logger.Int("depth", i))
			return nil, ctx.Err()
		}

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
			if len(val) == 0 {
				seen[path] = true
				continue
			}

			if c, err := cid.Cast(val); err == nil {
				arcs[path] = c
				seen[path] = true
			}
		}
		if err := iter.Err(); err != nil {
			iter.Close()
			logger.Error("EAT.collectFlattenedArcs iterator error",
				logger.String("bucket", bucketId),
				logger.Err(err))
			return nil, fmt.Errorf("iterator error: %w", err)
		}
		iter.Close()

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

	return arcs, nil
}

// Iterate returns a streaming iterator over all arcs visible at the given version.
func (e *EAT) Iterate(ctx context.Context, bucketId string, version cid.Cid) arcset.Iterator {
	return &chainIterator{
		eat:            e,
		ctx:            ctx,
		bucketId:       bucketId,
		currentVersion: version,
		seen:           make(map[string]bool),
		maxDepth:       1000,
	}
}

// Close releases resources.
func (e *EAT) Close() error {
	if e.bloomCache != nil {
		e.bloomCache.Clear()
	}
	return nil
}

// chainIterator walks the @previous chain to iterate all visible arcs.
type chainIterator struct {
	eat            *EAT
	ctx            context.Context
	bucketId       string
	currentVersion cid.Cid
	seen           map[string]bool
	maxDepth       int

	currentBatch map[string]cid.Cid
	currentKeys  []string
	keyIndex     int

	err error
}

func (it *chainIterator) Next() (string, cid.Cid, bool) {
	if it.ctx.Err() != nil {
		it.err = it.ctx.Err()
		return "", cid.Cid{}, false
	}

	for it.keyIndex < len(it.currentKeys) {
		path := it.currentKeys[it.keyIndex]
		it.keyIndex++

		if it.seen[path] {
			continue
		}
		it.seen[path] = true
		return path, it.currentBatch[path], true
	}

	if it.currentVersion == cid.Undef || it.maxDepth <= 0 {
		return "", cid.Cid{}, false
	}

	it.maxDepth--

	prefix := versionPrefix(it.bucketId, it.currentVersion)
	iter := it.eat.kv.NewIterator(it.ctx, prefix, nil)

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
		if len(val) == 0 {
			it.seen[path] = true
			continue
		}

		if c, err := cid.Cast(val); err == nil {
			it.currentBatch[path] = c
		}
	}
	if err := iter.Err(); err != nil {
		it.err = err
	}
	iter.Close()

	it.currentKeys = make([]string, 0, len(it.currentBatch))
	for p := range it.currentBatch {
		it.currentKeys = append(it.currentKeys, p)
	}
	it.keyIndex = 0

	it.currentVersion = nextVersion

	return it.Next()
}

func (it *chainIterator) Err() error {
	return it.err
}

func (it *chainIterator) Close() {}