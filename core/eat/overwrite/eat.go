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

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/kvstore"
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
func (e *EAT) Get(bucketId string, root cid.Cid, path string) (cid.Cid, error) {
	ctx := context.Background()

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := rootKey(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil {
			if err == kvstore.ErrNotFound {
				return cid.Cid{}, arcset.ErrNotFound
			}
			return cid.Cid{}, fmt.Errorf("failed to resolve root: %w", err)
		}

		// Verify this root maps to the correct bucket
		if string(bucketIdBytes) != bucketId {
			return cid.Cid{}, arcset.ErrNotFound
		}
	}

	// Get the arc
	arcKeyBytes := arcKey(bucketId, path)
	val, err := e.kv.Get(ctx, arcKeyBytes)
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Cid{}, arcset.ErrNotFound
		}
		return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
	}

	return cid.Cast(val)
}

// BatchGet retrieves multiple target CIDs in a single operation.
// Returns a map of path -> CID for paths that were found.
// Paths not found are omitted from the result map.
func (e *EAT) BatchGet(bucketId string, root cid.Cid, paths []string) (map[string]cid.Cid, error) {
	ctx := context.Background()

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := rootKey(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil {
			if err == kvstore.ErrNotFound {
				return nil, arcset.ErrNotFound
			}
			return nil, fmt.Errorf("failed to resolve root: %w", err)
		}

		// Verify this root maps to the correct bucket
		if string(bucketIdBytes) != bucketId {
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

	return results, nil
}

// Update stores arc entries with a new commitment root.
// If oldRoot is not cid.Undef, it invalidates the old root mapping.
// If newRoot is not cid.Undef, it creates a new root->bucketId mapping.
// If a target CID is cid.Undef, the corresponding arc is deleted.
func (e *EAT) Update(bucketId string, newRoot, oldRoot cid.Cid, arcs map[string]cid.Cid) error {
	ctx := context.Background()

	batch := e.kv.Batch()

	// Remove old root mapping if exists
	if oldRoot != cid.Undef {
		oldRootKey := rootKey(oldRoot)
		if err := batch.Delete(oldRootKey); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to delete old root mapping: %w", err)
		}
	}

	// Add new root mapping if provided
	if newRoot != cid.Undef {
		newRootKey := rootKey(newRoot)
		if err := batch.Put(newRootKey, []byte(bucketId)); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to add new root mapping: %w", err)
		}
	}

	// Add/Update/Delete arcs
	for path, target := range arcs {
		key := arcKey(bucketId, path)
		if target == cid.Undef {
			// Delete the arc
			if err := batch.Delete(key); err != nil {
				batch.Cancel()
				return fmt.Errorf("failed to delete arc %s: %w", path, err)
			}
		} else {
			// Add/Update the arc
			val := target.Bytes()
			if err := batch.Put(key, val); err != nil {
				batch.Cancel()
				return fmt.Errorf("failed to put arc %s: %w", path, err)
			}
		}
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit update: %w", err)
	}

	return nil
}

// Snapshot returns an immutable snapshot of all arcs in the bucket.
// If root is not cid.Undef, it validates that the root is valid for the bucket.
// If root is cid.Undef, validation is skipped.
func (e *EAT) Snapshot(bucketId string, root cid.Cid) arcset.Snapshot {
	ctx := context.Background()

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := rootKey(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(bucketIdBytes) != bucketId {
			return arcset.NewMap() // Return empty snapshot for invalid root
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

	return arcset.NewMapFrom(arcs)
}

// Iterate returns a streaming iterator over all arcs in the bucket.
// If root is not cid.Undef, it validates that the root is valid for the bucket.
// If root is cid.Undef, validation is skipped.
// Caller must call Close() on the iterator when done.
func (e *EAT) Iterate(bucketId string, root cid.Cid) arcset.Iterator {
	ctx := context.Background()

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