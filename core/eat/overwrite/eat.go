// Package overwrite provides a prototype EAT implementation with overwrite semantics.
// This EAT stores arc sets for single-root graphs with bucket-based isolation.
//
// Concurrency Control: This prototype does not implement distributed concurrency control.
// For production deployments, concurrency control should be handled at the interface layer
// (e.g., optimistic locking with CAS semantics) or by using a distributed KVStore backend
// (e.g., TiKV, CockroachDB) that provides transactional guarantees.
package overwrite

import (
	"context"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// EAT is a stateless EAT with overwrite semantics.
// It uses bucketId for namespace isolation, allowing multiple graphs
// to share the same KVStore instance.
// All operations are atomic via KVStore batch operations.
type EAT struct {
	kv kvstore.KVStore
}

// NewEAT creates a new EAT with the given KVStore.
func NewEAT(kv kvstore.KVStore) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{kv: kv}, nil
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

// Get retrieves the target CID for a path within a bucket.
// If root is not cid.Undef, it validates that the root is the current active root.
// If root is cid.Undef, validation is skipped.
func (e *EAT) Get(ctx context.Context, bucketId string, root cid.Cid, path string) (cid.Cid, error) {
	start := time.Now()

	logger.Debug("EAT.Get started",
		logger.String("bucket", bucketId),
		logger.String("root", root.String()),
		logger.String("path", path))

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
// Returns a map of path -> CID for paths that were found.
// Paths not found are omitted from the result map.
func (e *EAT) BatchGet(ctx context.Context, bucketId string, root cid.Cid, paths []string) (map[string]cid.Cid, error) {
	start := time.Now()

	logger.Debug("EAT.BatchGet started",
		logger.String("bucket", bucketId),
		logger.String("root", root.String()),
		logger.Int("path_count", len(paths)))

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
	keys := make([][]byte, len(paths))
	pathToKey := make(map[string]string, len(paths)) // maps path -> key string for result mapping
	for i, path := range paths {
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
		logger.Int("found_count", len(results)),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return results, nil
}

// Update stores arc entries with a new commitment root.
// If oldRoot is not cid.Undef, it invalidates the old root mapping.
// If newRoot is not cid.Undef, it creates a new root->bucketId mapping.
// If a target CID is cid.Undef, the corresponding arc is deleted.
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

	// Count deletions for logging
	deleteCount := 0

	// Add/Update/Delete arcs
	for path, target := range arcs {
		key := arcKey(bucketId, path)
		if target == cid.Undef {
			deleteCount++
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

	logger.Info("EAT.Update completed",
		logger.String("bucket", bucketId),
		logger.String("new_root", newRoot.String()),
		logger.Int("arc_count", len(arcs)),
		logger.Int("delete_count", deleteCount),
		logger.Bool("invalidated_old_root", oldRoot != cid.Undef),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return nil
}

// Snapshot returns an immutable snapshot of all arcs in the bucket.
// If root is not cid.Undef, it validates that the root is valid for the bucket.
// If root is cid.Undef, validation is skipped.
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
// If root is not cid.Undef, it validates that the root is valid for the bucket.
// If root is cid.Undef, validation is skipped.
// Caller must call Close() on the iterator when done.
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
	return nil
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

func (it *emptyIterator) Close() {
	// No resources to release
}