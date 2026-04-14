// Package arc provides compatibility adapters for the legacy ArcStore
// abstraction. The canonical MALT path uses EAT directly.
package arc

import (
	"context"
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// EATArcStore exposes root-scoped explicit arc storage through the optional
// ArcStore interface. It is a compatibility view over KV-backed arc state.
type EATArcStore struct {
	kv     kvstore.KVStore
	mu     sync.RWMutex
	closed bool

	// rootToBucket maps root CID to bucket ID for legacy compatibility
	rootToBucket map[string]string
}

// NewEATArcStore creates a compatibility ArcStore backed by KVStore.
func NewEATArcStore(kv kvstore.KVStore) *EATArcStore {
	return &EATArcStore{
		kv:           kv,
		rootToBucket: make(map[string]string),
	}
}

// arcKey generates the storage key for a path under a root.
// Format: root:path (root acts as bucket identifier)
func arcKey(root cid.Cid, path string) []byte {
	return []byte(root.String() + ":" + path)
}

// rootPrefix generates the prefix for all arcs under a root.
func rootPrefix(root cid.Cid) []byte {
	return []byte(root.String() + ":")
}

// rootMetadataKey generates the key for root metadata.
func rootMetadataKey(root cid.Cid) []byte {
	return []byte("root:meta:" + root.String())
}

// Get retrieves the target CID for a path under a root.
func (s *EATArcStore) Get(ctx context.Context, root cid.Cid, path string) (cid.Cid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return cid.Cid{}, interfaces.ErrStoreClosed
	}

	if !root.Defined() {
		return cid.Cid{}, fmt.Errorf("root must be defined")
	}

	key := arcKey(root, path)
	val, err := s.kv.Get(ctx, key)
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Cid{}, arcset.ErrNotFound
		}
		return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
	}

	return cid.Cast(val)
}

// BatchGet retrieves multiple target CIDs for paths under a root.
func (s *EATArcStore) BatchGet(ctx context.Context, root cid.Cid, paths []string) (map[string]cid.Cid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}

	if !root.Defined() {
		return nil, fmt.Errorf("root must be defined")
	}

	keys := make([][]byte, len(paths))
	pathToKey := make(map[string]string, len(paths))
	for i, path := range paths {
		key := arcKey(root, path)
		keys[i] = key
		pathToKey[string(key)] = path
	}

	kvResults, err := s.kv.BatchGet(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get arcs: %w", err)
	}

	results := make(map[string]cid.Cid)
	for keyStr, val := range kvResults {
		path := pathToKey[keyStr]
		if c, err := cid.Cast(val); err == nil {
			results[path] = c
		}
	}
	return results, nil
}

// Put stores an arc under a root.
func (s *EATArcStore) Put(ctx context.Context, root cid.Cid, path string, target cid.Cid) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return interfaces.ErrStoreClosed
	}

	if !root.Defined() {
		return fmt.Errorf("root must be defined")
	}

	key := arcKey(root, path)
	val := target.Bytes()
	return s.kv.Put(ctx, key, val)
}

// BatchPut stores multiple arcs under a root.
func (s *EATArcStore) BatchPut(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return interfaces.ErrStoreClosed
	}

	if !root.Defined() {
		return fmt.Errorf("root must be defined")
	}

	batch := s.kv.Batch()
	for path, target := range arcs {
		key := arcKey(root, path)
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
	return batch.Commit(ctx)
}

// Delete removes an arc under a root.
func (s *EATArcStore) Delete(ctx context.Context, root cid.Cid, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return interfaces.ErrStoreClosed
	}

	if !root.Defined() {
		return fmt.Errorf("root must be defined")
	}

	key := arcKey(root, path)
	return s.kv.Delete(ctx, key)
}

// Snapshot returns an immutable view of all arcs under a root.
func (s *EATArcStore) Snapshot(ctx context.Context, root cid.Cid) (arcset.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}

	if !root.Defined() {
		return nil, fmt.Errorf("root must be defined")
	}

	arcs := make(map[string]cid.Cid)
	prefix := rootPrefix(root)
	iter := s.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	for iter.Next() {
		key := iter.Key()
		// Extract path from key (remove "root:" prefix)
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

// Iterate returns an iterator over all arcs under a root.
func (s *EATArcStore) Iterate(ctx context.Context, root cid.Cid) arcset.Iterator {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()

	if closed || !root.Defined() {
		return &emptyIterator{}
	}

	prefix := rootPrefix(root)
	iter := s.kv.NewIterator(ctx, prefix, nil)

	return &arcIterator{
		iter:   iter,
		prefix: prefix,
	}
}

// Size returns the number of arcs under a root.
func (s *EATArcStore) Size(ctx context.Context, root cid.Cid) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return 0, interfaces.ErrStoreClosed
	}

	if !root.Defined() {
		return 0, fmt.Errorf("root must be defined")
	}

	count := 0
	prefix := rootPrefix(root)
	iter := s.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	for iter.Next() {
		count++
	}

	if err := iter.Err(); err != nil {
		return 0, fmt.Errorf("iterator error: %w", err)
	}

	return count, nil
}

// Close releases resources.
func (s *EATArcStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// arcIterator implements arcset.Iterator.
type arcIterator struct {
	iter   kvstore.Iterator
	prefix []byte
}

func (it *arcIterator) Next() (string, cid.Cid, bool) {
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

func (it *arcIterator) Err() error {
	return it.iter.Err()
}

func (it *arcIterator) Close() {
	it.iter.Close()
}

// emptyIterator is an empty iterator for invalid cases.
type emptyIterator struct{}

func (it *emptyIterator) Next() (string, cid.Cid, bool) {
	return "", cid.Cid{}, false
}

func (it *emptyIterator) Err() error {
	return nil
}

func (it *emptyIterator) Close() {}

// Ensure EATArcStore implements ArcStore.
var _ interfaces.ArcStore = (*EATArcStore)(nil)
