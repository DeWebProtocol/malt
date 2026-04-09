// Package versioned provides a versioned EAT implementation using a KVStore.
// Each version stores only modified arcs plus a @previous arc pointing to the parent version.
// Resolution walks the @previous chain to find arc entries.
//
// Bloom Filter: Each version stores a bloom filter as @bloom arc, containing all paths
// visible at that version (including ancestors). This enables fast negative lookups.
//
// Concurrency: This implementation is inherently concurrency-safe because each update
// creates a new version with its own namespace (bucketId:version:path).
package versioned

import (
	"context"
	"encoding/gob"
	"fmt"
	"bytes"
	"sync"
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
	BloomArc    = "@bloom"    // Serialized bloom filter for this version
)

// Default bloom filter parameters.
const (
	DefaultExpectedItems     = 10000
	DefaultFalsePositiveRate = 0.01
)

// EAT is a versioned EAT implementation with bucket-based isolation.
// Each version stores only modified arcs, with @previous linking to the parent.
type EAT struct {
	kv kvstore.KVStore

	// Bloom filter cache (bucketId:version -> bloom)
	bloomCache sync.Map // string -> *cachedBloom
	newBloom   func() bloom.BloomFilter
}

// cachedBloom holds a cached bloom filter.
type cachedBloom struct {
	filter bloom.BloomFilter
	mu     sync.RWMutex
}

// NewEAT creates a new versioned EAT with the given KVStore and default bloom parameters.
func NewEAT(kv kvstore.KVStore) (*EAT, error) {
	return NewEATWithBloomParams(kv, DefaultExpectedItems, DefaultFalsePositiveRate)
}

// NewEATWithBloomParams creates a new versioned EAT with custom bloom filter parameters.
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

// NewEATWithoutBloom creates a new versioned EAT without bloom filter optimization.
func NewEATWithoutBloom(kv kvstore.KVStore) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{
		kv:      kv,
		newBloom: nil, // disabled
	}, nil
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

// bloomCacheKey generates the cache key for a bloom filter.
func bloomCacheKey(bucketId string, version cid.Cid) string {
	return bucketId + ":" + version.String()
}

// MightContain checks if a path might exist at the given version using bloom filter.
// Returns false if the path definitely doesn't exist.
// Returns true if the path might exist (need to call Get to verify).
func (e *EAT) MightContain(ctx context.Context, bucketId string, version cid.Cid, path string) bool {
	if e.newBloom == nil {
		return true // Bloom disabled
	}

	cb := e.loadBloom(ctx, bucketId, version)
	if cb == nil {
		return true // No bloom available
	}

	cb.mu.RLock()
	result := cb.filter.Test([]byte(path))
	cb.mu.RUnlock()

	return result
}

// MightContainBatch checks multiple paths at once using bloom filter.
func (e *EAT) MightContainBatch(ctx context.Context, bucketId string, version cid.Cid, paths []string) map[string]bool {
	result := make(map[string]bool, len(paths))

	if e.newBloom == nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	cb := e.loadBloom(ctx, bucketId, version)
	if cb == nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	cb.mu.RLock()
	for _, p := range paths {
		result[p] = cb.filter.Test([]byte(p))
	}
	cb.mu.RUnlock()

	return result
}

// loadBloom loads the bloom filter for a version, from cache or @bloom arc.
func (e *EAT) loadBloom(ctx context.Context, bucketId string, version cid.Cid) *cachedBloom {
	cacheKey := bloomCacheKey(bucketId, version)

	// Check cache first
	if val, ok := e.bloomCache.Load(cacheKey); ok {
		return val.(*cachedBloom)
	}

	// Try to load from @bloom arc
	key := arcKey(bucketId, version, BloomArc)
	val, err := e.kv.Get(ctx, key)
	if err != nil {
		// No @bloom arc, return nil
		return nil
	}

	// Deserialize bloom filter
	filter, err := deserializeBloom(val)
	if err != nil {
		logger.Warn("EAT.loadBloom failed to deserialize",
			logger.String("bucket", bucketId),
			logger.String("version", version.String()),
			logger.Err(err))
		return nil
	}

	// Cache it
	cb := &cachedBloom{filter: filter}
	e.bloomCache.Store(cacheKey, cb)

	return cb
}

// serializeBloom serializes a bloom filter to bytes.
func serializeBloom(filter bloom.BloomFilter) ([]byte, error) {
	// For StandardBloom, we serialize the bitset
	sf, ok := filter.(*bloom.StandardBloom)
	if !ok {
		return nil, fmt.Errorf("unsupported bloom filter type")
	}

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)

	// Encode bloom parameters
	if err := enc.Encode(sf.K()); err != nil {
		return nil, err
	}
	if err := enc.Encode(sf.M()); err != nil {
		return nil, err
	}
	if err := enc.Encode(sf.Bitset()); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// deserializeBloom deserializes a bloom filter from bytes.
func deserializeBloom(data []byte) (*bloom.StandardBloom, error) {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)

	var k, m uint
	var bitsetBytes []byte

	if err := dec.Decode(&k); err != nil {
		return nil, err
	}
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if err := dec.Decode(&bitsetBytes); err != nil {
		return nil, err
	}

	return bloom.NewStandardBloomFromData(k, m, bitsetBytes)
}

// collectPaths collects all visible paths at a version (including ancestors).
func (e *EAT) collectPaths(ctx context.Context, bucketId string, version cid.Cid) ([]string, error) {
	paths := make([]string, 0)
	seen := make(map[string]bool)
	currentVersion := version
	maxDepth := 1000

	for i := 0; i < maxDepth; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		prefix := versionPrefix(bucketId, currentVersion)
		iter := e.kv.NewIterator(ctx, prefix, nil)

		for iter.Next() {
			key := iter.Key()
			path := string(key[len(prefix):])

			// Skip reserved arcs
			if path == PreviousArc || path == BloomArc {
				continue
			}

			if seen[path] {
				continue
			}

			// Check for tombstone
			val := iter.Value()
			if len(val) == 0 {
				seen[path] = true
				continue
			}

			paths = append(paths, path)
			seen[path] = true
		}

		if err := iter.Err(); err != nil {
			iter.Close()
			return nil, fmt.Errorf("iterator error: %w", err)
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

	return paths, nil
}

// Get retrieves the target CID for a path at a specific version.
// First checks bloom filter, then walks the @previous chain.
func (e *EAT) Get(ctx context.Context, bucketId string, version cid.Cid, path string) (cid.Cid, error) {
	start := time.Now()

	// Quick bloom filter check
	if !e.MightContain(ctx, bucketId, version, path) {
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
	mightExist := e.MightContainBatch(ctx, bucketId, version, paths)
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

// Update stores arcs at a new version and builds the bloom filter.
func (e *EAT) Update(ctx context.Context, bucketId string, newRoot, parentRoot cid.Cid, arcs map[string]cid.Cid) error {
	start := time.Now()

	logger.Info("EAT.Update started",
		logger.String("bucket", bucketId),
		logger.String("new_root", newRoot.String()),
		logger.String("parent_root", parentRoot.String()),
		logger.Int("arc_count", len(arcs)))

	batch := e.kv.Batch()

	tombstoneCount := 0

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

	// Commit first so bloom collection can see the data
	if err := batch.Commit(ctx); err != nil {
		logger.Error("EAT.Update commit failed",
			logger.String("bucket", bucketId),
			logger.Err(err))
		return fmt.Errorf("failed to commit version: %w", err)
	}

	// Build and store bloom filter after commit
	if e.newBloom != nil {
		if err := e.buildAndStoreBloomAfterCommit(ctx, bucketId, newRoot); err != nil {
			logger.Warn("EAT.Update failed to build bloom (non-fatal)",
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

// buildAndStoreBloomAfterCommit builds and stores bloom after the batch is committed.
func (e *EAT) buildAndStoreBloomAfterCommit(ctx context.Context, bucketId string, version cid.Cid) error {
	// Collect all visible paths at this version
	paths, err := e.collectPaths(ctx, bucketId, version)
	if err != nil {
		return err
	}

	// Build bloom filter
	filter := e.newBloom()
	for _, path := range paths {
		filter.Add([]byte(path))
	}

	// Serialize and store
	data, err := serializeBloom(filter)
	if err != nil {
		return fmt.Errorf("failed to serialize bloom: %w", err)
	}

	key := arcKey(bucketId, version, BloomArc)
	if err := e.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("failed to store @bloom: %w", err)
	}

	// Cache the bloom
	cb := &cachedBloom{filter: filter}
	e.bloomCache.Store(bloomCacheKey(bucketId, version), cb)

	logger.Debug("EAT.buildAndStoreBloomAfterCommit completed",
		logger.String("bucket", bucketId),
		logger.String("version", version.String()),
		logger.Int("path_count", len(paths)))

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

			if path == PreviousArc || path == BloomArc {
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
	e.bloomCache.Range(func(key, value interface{}) bool {
		e.bloomCache.Delete(key)
		return true
	})
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

		if path == BloomArc {
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