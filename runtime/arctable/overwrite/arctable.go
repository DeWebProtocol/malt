// Package overwrite provides an ArcTable implementation with overwrite semantics.
// This ArcTable stores arc sets for single-root graphs with namespace-based isolation.
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

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/logger"
	"github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/runtime/arctable/bloom"
	"github.com/dewebprotocol/malt/storage/kv"
	cid "github.com/ipfs/go-cid"
)

// ArcTable is an ArcTable with overwrite semantics.
// It uses namespace for namespace isolation, allowing multiple graphs
// to share the same KVStore instance.
type ArcTable struct {
	kv           kvstore.KVStore
	bloomManager *arctable.BloomFilterManager // Uses common BloomFilterManager
}

// NewArcTable creates a new ArcTable with the given KVStore and optional configuration.
// Bloom filter is disabled by default; use WithBloomCache to enable it.
//
// Example usage:
//
//	// Simple: use defaults
//	arctable, _ := overwrite.NewArcTable(overwrite.WithKVStore(kv))
//
//	// With bloom cache
//	arctable, _ := overwrite.NewArcTable(
//	    overwrite.WithKVStore(kv),
//	    overwrite.WithBloomCache(bloomCache),
//	)
func NewArcTable(opts ...Option) (*ArcTable, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	if o.kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	bloomManager := arctable.NewBloomFilterManager(o.bloomCache)

	return &ArcTable{
		kv:           o.kv,
		bloomManager: bloomManager,
	}, nil
}

// NewArcTableWithBloomCache creates a new ArcTable with BloomCache for fast negative lookups.
// Deprecated: Use NewArcTable with WithBloomCache option instead.
func NewArcTableWithBloomCache(kv kvstore.KVStore, bloomCache *bloom.BloomCache) (*ArcTable, error) {
	return NewArcTable(WithKVStore(kv), WithBloomCache(bloomCache))
}

// CreateNamespace creates a new namespace with custom bloom configuration.
func (e *ArcTable) CreateNamespace(ctx context.Context, namespace string, cfg *bloom.NamespaceConfig) error {
	return e.bloomManager.CreateNamespace(ctx, namespace, cfg)
}

// MightContain checks if a path might exist in the namespace using bloom filter.
// Returns false if the path definitely doesn't exist (can skip KVStore lookup).
// Returns true if the path might exist (need to call Get to verify).
func (e *ArcTable) MightContain(ctx context.Context, namespace string, path arcset.Path) bool {
	if !e.bloomManager.Enabled() {
		return true // Bloom disabled
	}
	return e.bloomManager.MightContain(namespace, path.String())
}

// MightContainBatch checks multiple paths at once using bloom filter.
func (e *ArcTable) MightContainBatch(ctx context.Context, namespace string, paths []arcset.Path) map[arcset.Path]bool {
	result := make(map[arcset.Path]bool, len(paths))

	if !e.bloomManager.Enabled() {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	pathStrings := make([]string, len(paths))
	for i, path := range paths {
		pathStrings[i] = path.String()
	}
	batchResult, err := e.bloomManager.MightContainBatch(ctx, namespace, pathStrings)
	if err != nil {
		for _, p := range paths {
			result[p] = true
		}
		return result
	}

	for _, path := range paths {
		result[path] = batchResult[path.String()]
	}
	return result
}

// Get retrieves the target CID for a path within a namespace.
// First checks bloom filter, then queries KVStore.
func (e *ArcTable) Get(ctx context.Context, namespace string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	start := time.Now()

	logger.Debug("ArcTable.Get started",
		logger.String("namespace", namespace),
		logger.String("root", root.String()),
		logger.String("path", path.String()))

	// Quick bloom filter check
	if !e.MightContain(ctx, namespace, path) {
		logger.Debug("ArcTable.Get bloom negative",
			logger.String("namespace", namespace),
			logger.String("path", path.String()))
		return cid.Cid{}, arcset.ErrNotFound
	}

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := arctable.RootKeyFormat(root)
		namespaceBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil {
			if err == kvstore.ErrNotFound {
				logger.Debug("ArcTable.Get root not found",
					logger.String("namespace", namespace),
					logger.String("root", root.String()))
				return cid.Cid{}, arcset.ErrNotFound
			}
			logger.Error("ArcTable.Get failed to resolve root",
				logger.String("namespace", namespace),
				logger.String("root", root.String()),
				logger.Err(err))
			return cid.Cid{}, fmt.Errorf("failed to resolve root: %w", err)
		}

		// Verify this root maps to the correct namespace
		if string(namespaceBytes) != namespace {
			logger.Debug("ArcTable.Get root namespace mismatch",
				logger.String("namespace", namespace),
				logger.String("root", root.String()),
				logger.String("expected_namespace", string(namespaceBytes)))
			return cid.Cid{}, arcset.ErrNotFound
		}
	}

	// Get the arc
	arcKeyBytes := arctable.DefaultArcKey(namespace, path)
	val, err := e.kv.Get(ctx, arcKeyBytes)
	if err != nil {
		if err == kvstore.ErrNotFound {
			logger.Debug("ArcTable.Get arc not found",
				logger.String("namespace", namespace),
				logger.String("path", path.String()))
			return cid.Cid{}, arcset.ErrNotFound
		}
		logger.Error("ArcTable.Get failed to get arc",
			logger.String("namespace", namespace),
			logger.String("path", path.String()),
			logger.Err(err))
		return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
	}

	result, err := cid.Cast(val)
	if err == nil {
		logger.Debug("ArcTable.Get success",
			logger.String("namespace", namespace),
			logger.String("path", path.String()),
			logger.String("target", result.String()),
			logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))
	}
	return result, err
}

// BatchGet retrieves multiple target CIDs in a single operation.
// Uses bloom filter to filter out definitely-not-present paths.
func (e *ArcTable) BatchGet(ctx context.Context, namespace string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	start := time.Now()

	logger.Debug("ArcTable.BatchGet started",
		logger.String("namespace", namespace),
		logger.String("root", root.String()),
		logger.Int("path_count", len(paths)))

	// Filter paths using bloom filter
	mightExist := e.MightContainBatch(ctx, namespace, paths)
	filteredPaths := make([]arcset.Path, 0, len(paths))
	for _, p := range paths {
		if mightExist[p] {
			filteredPaths = append(filteredPaths, p)
		}
	}

	if len(filteredPaths) == 0 {
		logger.Debug("ArcTable.BatchGet all filtered by bloom",
			logger.String("namespace", namespace))
		return map[arcset.Path]cid.Cid{}, nil
	}

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := arctable.RootKeyFormat(root)
		namespaceBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil {
			if err == kvstore.ErrNotFound {
				logger.Debug("ArcTable.BatchGet root not found",
					logger.String("namespace", namespace),
					logger.String("root", root.String()))
				return nil, arcset.ErrNotFound
			}
			logger.Error("ArcTable.BatchGet failed to resolve root",
				logger.String("namespace", namespace),
				logger.Err(err))
			return nil, fmt.Errorf("failed to resolve root: %w", err)
		}

		// Verify this root maps to the correct namespace
		if string(namespaceBytes) != namespace {
			logger.Debug("ArcTable.BatchGet root namespace mismatch",
				logger.String("namespace", namespace),
				logger.String("expected_namespace", string(namespaceBytes)))
			return nil, arcset.ErrNotFound
		}
	}

	// Build keys for batch get
	keys := make([][]byte, len(filteredPaths))
	pathToKey := make(map[string]arcset.Path, len(filteredPaths))
	for i, path := range filteredPaths {
		key := arctable.DefaultArcKey(namespace, path)
		keys[i] = key
		pathToKey[string(key)] = path
	}

	// Use KVStore BatchGet for efficient bulk retrieval
	kvResults, err := e.kv.BatchGet(ctx, keys)
	if err != nil {
		logger.Error("ArcTable.BatchGet kv error",
			logger.String("namespace", namespace),
			logger.Err(err))
		return nil, fmt.Errorf("failed to batch get arcs: %w", err)
	}

	// Convert results to CID map
	results := make(map[arcset.Path]cid.Cid)
	for keyStr, val := range kvResults {
		path := pathToKey[keyStr]
		if c, err := cid.Cast(val); err == nil {
			results[path] = c
		}
	}

	logger.Debug("ArcTable.BatchGet completed",
		logger.String("namespace", namespace),
		logger.Int("requested_count", len(paths)),
		logger.Int("filtered_count", len(filteredPaths)),
		logger.Int("found_count", len(results)),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return results, nil
}

// Update stores arc entries with a new commitment root.
// Updates the namespace bloom filter incrementally.
func (e *ArcTable) Update(ctx context.Context, namespace string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error {
	arcMap, err := arcset.ToPathMap(arcs)
	if err != nil {
		return err
	}
	start := time.Now()

	logger.Info("ArcTable.Update started",
		logger.String("namespace", namespace),
		logger.String("new_root", newRoot.String()),
		logger.String("old_root", oldRoot.String()),
		logger.Int("arc_count", len(arcMap)))

	batch := e.kv.Batch()

	// Remove old root mapping if exists
	if oldRoot != cid.Undef {
		oldRootKey := arctable.RootKeyFormat(oldRoot)
		if err := batch.Delete(oldRootKey); err != nil {
			batch.Cancel()
			logger.Error("ArcTable.Update failed to delete old root",
				logger.String("namespace", namespace),
				logger.String("old_root", oldRoot.String()),
				logger.Err(err))
			return fmt.Errorf("failed to delete old root mapping: %w", err)
		}
	}

	// Add new root mapping if provided
	if newRoot != cid.Undef {
		newRootKey := arctable.RootKeyFormat(newRoot)
		if err := batch.Put(newRootKey, []byte(namespace)); err != nil {
			batch.Cancel()
			logger.Error("ArcTable.Update failed to add new root",
				logger.String("namespace", namespace),
				logger.String("new_root", newRoot.String()),
				logger.Err(err))
			return fmt.Errorf("failed to add new root mapping: %w", err)
		}
	}

	// Track added paths for bloom filter update
	var addedPaths []string

	// Add/Update/Delete arcs
	for path, target := range arcMap {
		key := arctable.DefaultArcKey(namespace, path)
		if target == cid.Undef {
			// Delete the arc
			if err := batch.Delete(key); err != nil {
				batch.Cancel()
				logger.Error("ArcTable.Update failed to delete arc",
					logger.String("namespace", namespace),
					logger.String("path", path.String()),
					logger.Err(err))
				return fmt.Errorf("failed to delete arc %s: %w", path.String(), err)
			}
		} else {
			addedPaths = append(addedPaths, path.String())
			// Add/Update the arc
			val := target.Bytes()
			if err := batch.Put(key, val); err != nil {
				batch.Cancel()
				logger.Error("ArcTable.Update failed to put arc",
					logger.String("namespace", namespace),
					logger.String("path", path.String()),
					logger.Err(err))
				return fmt.Errorf("failed to put arc %s: %w", path.String(), err)
			}
		}
	}

	if err := batch.Commit(ctx); err != nil {
		logger.Error("ArcTable.Update commit failed",
			logger.String("namespace", namespace),
			logger.Err(err))
		return fmt.Errorf("failed to commit update: %w", err)
	}

	// Update namespace bloom filter after successful commit
	if e.bloomManager.Enabled() && len(addedPaths) > 0 {
		if err := e.bloomManager.AddBatch(ctx, namespace, addedPaths); err != nil {
			logger.Warn("ArcTable.Update failed to update bloom (non-fatal)",
				logger.String("namespace", namespace),
				logger.Err(err))
			// Non-fatal: bloom is optional optimization
		}
	}

	logger.Info("ArcTable.Update completed",
		logger.String("namespace", namespace),
		logger.String("new_root", newRoot.String()),
		logger.Int("arc_count", len(arcMap)),
		logger.Int("added_count", len(addedPaths)),
		logger.Bool("invalidated_old_root", oldRoot != cid.Undef),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return nil
}

// Snapshot returns an immutable snapshot of all arcs in the namespace.
func (e *ArcTable) Snapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error) {
	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := arctable.RootKeyFormat(root)
		namespaceBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(namespaceBytes) != namespace {
			return arcset.NewSet(), nil // Return empty snapshot for invalid root
		}
	}

	// Load all arcs into memory
	arcs := make(map[string]cid.Cid)
	prefix := arctable.DefaultNamespacePrefix(namespace)
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

	snapshot, err := arcset.NewArcSet(arcs)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Iterate returns a streaming iterator over all arcs in the namespace.
func (e *ArcTable) Iterate(ctx context.Context, namespace string, root cid.Cid) arcset.Iterator {
	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := arctable.RootKeyFormat(root)
		namespaceBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(namespaceBytes) != namespace {
			return &emptyIterator{}
		}
	}

	prefix := arctable.DefaultNamespacePrefix(namespace)
	iter := e.kv.NewIterator(ctx, prefix, nil)

	return &arcTableIterator{
		iter:   iter,
		prefix: prefix,
	}
}

// Close releases resources.
func (e *ArcTable) Close() error {
	if bc := e.bloomManager.GetBloomCache(); bc != nil {
		bc.Clear()
	}
	return nil
}

// Stats returns bloom filter cache statistics.
func (e *ArcTable) Stats() map[string]interface{} {
	if !e.bloomManager.Enabled() {
		return map[string]interface{}{
			"bloom_enabled": false,
		}
	}
	return map[string]interface{}{
		"bloom_enabled": true,
		"cache_size":    e.bloomManager.GetBloomCache().Size(),
	}
}

// arcTableIterator implements arcset.Iterator for the ArcTable.
type arcTableIterator struct {
	iter   kvstore.Iterator
	prefix []byte
}

func (it *arcTableIterator) Next() (arcset.Path, cid.Cid, bool) {
	for {
		if !it.iter.Next() {
			return "", cid.Cid{}, false
		}

		key := it.iter.Key()
		path := arcset.CanonicalizePath(string(key[len(it.prefix):]))

		val := it.iter.Value()
		c, err := cid.Cast(val)
		if err != nil {
			continue // Skip invalid entries
		}

		return path, c, true
	}
}

func (it *arcTableIterator) Err() error {
	return it.iter.Err()
}

func (it *arcTableIterator) Close() {
	it.iter.Close()
}

// emptyIterator is an empty iterator for invalid root cases.
type emptyIterator struct{}

func (it *emptyIterator) Next() (arcset.Path, cid.Cid, bool) {
	return "", cid.Cid{}, false
}

func (it *emptyIterator) Err() error {
	return nil
}

func (it *emptyIterator) Close() {}
