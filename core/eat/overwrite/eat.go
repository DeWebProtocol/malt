// Package overwrite provides an EAT implementation with overwrite semantics.
// This EAT stores arc sets with bucket-based isolation and overwrite semantics.
package overwrite

import (
	"context"
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/kvstore"
	cid "github.com/ipfs/go-cid"
)

// Option is a configuration option for EAT.
type Option func(*EAT)

// WithSnapshotView configures EAT to create snapshot views.
// When enabled, View() returns a snapshot of the data at that point in time,
// ensuring consistency even if the underlying data changes.
func WithSnapshotView(snapshot bool) Option {
	return func(e *EAT) {
		e.snapshotView = snapshot
	}
}

// EAT is a stateless EAT with overwrite semantics.
// It uses bucketId for namespace isolation, allowing multiple graphs
// to share the same KVStore instance.
type EAT struct {
	mu           sync.RWMutex
	kv           kvstore.KVStore
	snapshotView bool // if true, View creates a snapshot instead of live view
}

// NewEAT creates a new EAT with the given KVStore.
// Options can be provided to configure the EAT behavior.
func NewEAT(kv kvstore.KVStore, opts ...Option) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	e := &EAT{
		kv: kv,
	}

	for _, opt := range opts {
		opt(e)
	}

	return e, nil
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

	e.mu.RLock()
	defer e.mu.RUnlock()

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

// Update stores arc entries with a new commitment root.
// If oldRoot is not cid.Undef, it invalidates the old root mapping.
// If newRoot is not cid.Undef, it creates a new root->bucketId mapping.
// If a target CID is cid.Undef, the corresponding arc is deleted.
func (e *EAT) Update(bucketId string, newRoot, oldRoot cid.Cid, arcs map[string]cid.Cid) error {
	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

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

// View returns an ArcSetView for a specific bucket and root.
// If root is not cid.Undef, it validates that the root is valid for the bucket.
// If root is cid.Undef, validation is skipped.
// If snapshotView is enabled, returns a snapshot of the data at this point in time.
func (e *EAT) View(bucketId string, root cid.Cid) arcset.View {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Validate root if provided
	if root != cid.Undef {
		rootKeyBytes := rootKey(root)
		bucketIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
		if err != nil || string(bucketIdBytes) != bucketId {
			return &emptyView{}
		}
	}

	if e.snapshotView {
		// Create a snapshot by copying all arcs into memory
		return e.createSnapshotView(bucketId)
	}

	return &eatView{eat: e, bucketId: bucketId}
}

// createSnapshotView creates a snapshot view by copying all arcs into memory.
func (e *EAT) createSnapshotView(bucketId string) arcset.View {
	ctx := context.Background()
	arcs := make(map[string]cid.Cid)

	prefix := bucketPrefix(bucketId)
	iter := e.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	for iter.Next() {
		key := iter.Key()
		path := string(key[len(prefix):])
		val := iter.Value()
		c, err := cid.Cast(val)
		if err == nil {
			arcs[path] = c
		}
	}

	return arcset.NewMapFrom(arcs)
}

// Iterate returns an iterator over all arcs in a bucket.
func (e *EAT) Iterate(bucketId string) arcset.Iterator {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	prefix := bucketPrefix(bucketId)
	iter := e.kv.NewIterator(ctx, prefix, nil)

	return &eatIterator{
		iter:   iter,
		prefix: prefix,
	}
}

// Len returns the number of arcs in a bucket.
func (e *EAT) Len(bucketId string) int {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	prefix := bucketPrefix(bucketId)
	iter := e.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	count := 0
	for iter.Next() {
		count++
	}

	return count
}

// Clear removes all arcs in a bucket.
// Note: This does not remove root mappings.
func (e *EAT) Clear(bucketId string) error {
	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Collect all keys to delete
	var keys [][]byte

	prefix := bucketPrefix(bucketId)
	iter := e.kv.NewIterator(ctx, prefix, nil)
	for iter.Next() {
		keys = append(keys, iter.Key())
	}
	iter.Close()

	// Delete in batch
	batch := e.kv.Batch()
	for _, key := range keys {
		if err := batch.Delete(key); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to add delete to batch: %w", err)
		}
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to clear: %w", err)
	}

	return nil
}

// Close releases resources.
func (e *EAT) Close() error {
	return nil
}

// eatView implements arcset.View for the EAT.
type eatView struct {
	eat      *EAT
	bucketId string
}

func (v *eatView) Get(path string) (cid.Cid, bool) {
	ctx := context.Background()
	val, err := v.eat.kv.Get(ctx, arcKey(v.bucketId, path))
	if err != nil {
		return cid.Cid{}, false
	}
	c, err := cid.Cast(val)
	if err != nil {
		return cid.Cid{}, false
	}
	return c, true
}

func (v *eatView) Iterate() arcset.Iterator {
	return v.eat.Iterate(v.bucketId)
}

func (v *eatView) Len() int {
	return v.eat.Len(v.bucketId)
}

// eatIterator implements arcset.Iterator for the EAT.
type eatIterator struct {
	iter   kvstore.Iterator
	prefix []byte
}

func (it *eatIterator) Next() (string, cid.Cid, bool) {
	if !it.iter.Next() {
		return "", cid.Cid{}, false
	}

	key := it.iter.Key()
	// Extract path from key: bucketId:path -> path
	path := string(key[len(it.prefix):])

	val := it.iter.Value()
	c, err := cid.Cast(val)
	if err != nil {
		return it.Next() // Skip invalid entries
	}

	return path, c, true
}

func (it *eatIterator) Err() error {
	return it.iter.Err()
}

// emptyView is an empty view.
type emptyView struct{}

func (v *emptyView) Get(path string) (cid.Cid, bool) {
	return cid.Cid{}, false
}

func (v *emptyView) Iterate() arcset.Iterator {
	return &emptyIterator{}
}

func (v *emptyView) Len() int {
	return 0
}

// emptyIterator is an empty iterator.
type emptyIterator struct{}

func (it *emptyIterator) Next() (string, cid.Cid, bool) {
	return "", cid.Cid{}, false
}

func (it *emptyIterator) Err() error {
	return nil
}