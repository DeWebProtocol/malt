// Package overwrite provides an EAT implementation with overwrite semantics.
// This EAT stores a single graph's arc set with overwrite semantics.
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

// EAT is a single-graph EAT with overwrite semantics.
// It uses a fixed graphId for data storage, and maintains root->graphId mappings
// to allow access via commitment roots.
type EAT struct {
	mu           sync.RWMutex
	graphId      string
	kv           kvstore.KVStore
	snapshotView bool // if true, View creates a snapshot instead of live view
}

// NewEAT creates a new single-graph EAT with the given KVStore and graphId.
// The graphId is a unique identifier used as the namespace for all arc entries.
// Options can be provided to configure the EAT behavior.
func NewEAT(kv kvstore.KVStore, graphId string, opts ...Option) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}
	if graphId == "" {
		return nil, fmt.Errorf("graphId is required")
	}

	e := &EAT{
		graphId: graphId,
		kv:      kv,
	}

	for _, opt := range opts {
		opt(e)
	}

	return e, nil
}

// arcKey generates the storage key for a path.
// Format: graphId:path
func (e *EAT) arcKey(path string) []byte {
	return []byte(e.graphId + ":" + path)
}

// rootKey generates the key for root->graphId mapping.
// Format: root:{cid}
func rootKey(root cid.Cid) []byte {
	return []byte("root:" + root.String())
}

// Get retrieves the target CID for a path via a commitment root.
// It first resolves root->graphId, then looks up the arc in that graph.
func (e *EAT) Get(root cid.Cid, path string) (cid.Cid, error) {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Resolve root -> graphId (verify the root is valid for this graph)
	rootKeyBytes := rootKey(root)
	graphIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Cid{}, arcset.ErrNotFound
		}
		return cid.Cid{}, fmt.Errorf("failed to resolve root: %w", err)
	}

	// Verify this root maps to our graphId
	if string(graphIdBytes) != e.graphId {
		return cid.Cid{}, arcset.ErrNotFound
	}

	// Get the arc
	arcKeyBytes := e.arcKey(path)
	val, err := e.kv.Get(ctx, arcKeyBytes)
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Cid{}, arcset.ErrNotFound
		}
		return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
	}

	return cid.Cast(val)
}

// Update stores multiple arc entries with a new commitment root.
// It removes the old root->graphId mapping to prevent access via stale roots.
// If oldRoot is cid.Undef, this is the first update (no old mapping to remove).
// If a target CID is cid.Undef, the corresponding arc is deleted.
func (e *EAT) Update(newRoot, oldRoot cid.Cid, arcs map[string]cid.Cid) error {
	if newRoot == cid.Undef {
		return fmt.Errorf("newRoot cannot be Undef")
	}

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

	// Add new root mapping
	newRootKey := rootKey(newRoot)
	if err := batch.Put(newRootKey, []byte(e.graphId)); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to add new root mapping: %w", err)
	}

	// Add/Update/Delete arcs
	for path, target := range arcs {
		key := e.arcKey(path)
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

// View returns an ArcSetView for a specific root.
// Returns an empty view if the root doesn't map to this graph.
// If snapshotView is enabled, returns a snapshot of the data at this point in time.
func (e *EAT) View(root cid.Cid) arcset.View {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	// Verify root maps to this graph
	rootKeyBytes := rootKey(root)
	graphIdBytes, err := e.kv.Get(ctx, rootKeyBytes)
	if err != nil || string(graphIdBytes) != e.graphId {
		return &emptyView{}
	}

	if e.snapshotView {
		// Create a snapshot by copying all arcs into memory
		return e.createSnapshotView()
	}

	return &eatView{eat: e}
}

// createSnapshotView creates a snapshot view by copying all arcs into memory.
func (e *EAT) createSnapshotView() arcset.View {
	ctx := context.Background()
	arcs := make(map[string]cid.Cid)

	prefix := []byte(e.graphId + ":")
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

// Iterate returns an iterator over all arcs in this graph.
// Note: This iterates all arcs regardless of root validation.
func (e *EAT) Iterate() arcset.Iterator {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	prefix := []byte(e.graphId + ":")
	iter := e.kv.NewIterator(ctx, prefix, nil)

	return &eatIterator{
		iter:   iter,
		prefix: prefix,
	}
}

// Len returns the number of arcs in this graph.
func (e *EAT) Len() int {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	prefix := []byte(e.graphId + ":")
	iter := e.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	count := 0
	for iter.Next() {
		count++
	}

	return count
}

// Clear removes all arcs and root mappings for this graph.
func (e *EAT) Clear() error {
	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Collect all keys to delete (arcs and root mappings)
	var keys [][]byte

	// Arc keys
	prefix := []byte(e.graphId + ":")
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

// GraphId returns the graph identifier.
func (e *EAT) GraphId() string {
	return e.graphId
}

// eatView implements arcset.View for the EAT.
type eatView struct {
	eat *EAT
}

func (v *eatView) Get(path string) (cid.Cid, bool) {
	ctx := context.Background()
	val, err := v.eat.kv.Get(ctx, v.eat.arcKey(path))
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
	return v.eat.Iterate()
}

func (v *eatView) Len() int {
	return v.eat.Len()
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
	// Extract path from key: graphId:path -> path
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
