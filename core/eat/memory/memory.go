// Package memory provides in-memory implementations for EAT and arc sets.
package memory

import (
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat"
	cid "github.com/ipfs/go-cid"
)

// InMemoryArcSet is a single-graph arc set that stores only the latest version.
// It implements arcset.View for use with SCE commitment operations.
type InMemoryArcSet struct {
	mu   sync.RWMutex
	arcs map[string]cid.Cid
}

// NewInMemoryArcSet creates a new InMemoryArcSet.
func NewInMemoryArcSet() *InMemoryArcSet {
	return &InMemoryArcSet{
		arcs: make(map[string]cid.Cid),
	}
}

// Set adds or updates an arc.
func (a *InMemoryArcSet) Set(path string, target cid.Cid) {
	a.mu.Lock()
	a.arcs[path] = target
	a.mu.Unlock()
}

// Get retrieves the target CID for a path.
func (a *InMemoryArcSet) Get(path string) (cid.Cid, bool) {
	a.mu.RLock()
	c, ok := a.arcs[path]
	a.mu.RUnlock()
	return c, ok
}

// Delete removes an arc.
func (a *InMemoryArcSet) Delete(path string) {
	a.mu.Lock()
	delete(a.arcs, path)
	a.mu.Unlock()
}

// Iterate returns an iterator over all arcs.
// The iterator captures a snapshot of paths at creation time.
func (a *InMemoryArcSet) Iterate() arcset.Iterator {
	a.mu.RLock()
	// Snapshot paths while holding lock
	paths := make([]string, 0, len(a.arcs))
	arcs := make(map[string]cid.Cid, len(a.arcs))
	for p, c := range a.arcs {
		paths = append(paths, p)
		arcs[p] = c
	}
	a.mu.RUnlock()

	sort.Strings(paths)
	return &arcSetIterator{arcs: arcs, paths: paths, idx: -1}
}

// Len returns the number of arcs.
func (a *InMemoryArcSet) Len() int {
	a.mu.RLock()
	n := len(a.arcs)
	a.mu.RUnlock()
	return n
}

// Clear removes all arcs.
func (a *InMemoryArcSet) Clear() {
	a.mu.Lock()
	a.arcs = make(map[string]cid.Cid)
	a.mu.Unlock()
}

// arcSetIterator implements arcset.Iterator with a snapshot.
type arcSetIterator struct {
	arcs  map[string]cid.Cid
	paths []string
	idx   int
}

// Next advances to the next arc.
func (it *arcSetIterator) Next() (string, cid.Cid, bool) {
	it.idx++
	if it.idx >= len(it.paths) {
		return "", cid.Cid{}, false
	}
	path := it.paths[it.idx]
	return path, it.arcs[path], true
}

// Err returns any error.
func (it *arcSetIterator) Err() error {
	return nil
}

// Ensure InMemoryArcSet implements arcset.View.
var _ arcset.View = (*InMemoryArcSet)(nil)

// BucketedInMemoryEAT manages multiple InMemoryArcSet instances by bucket (graph) ID.
// It implements eat.EAT for use as an in-memory EAT backend.
type BucketedInMemoryEAT struct {
	mu      sync.RWMutex
	buckets map[string]*InMemoryArcSet // bucket key -> arc set
}

// NewBucketedInMemoryEAT creates a new BucketedInMemoryEAT.
func NewBucketedInMemoryEAT() *BucketedInMemoryEAT {
	return &BucketedInMemoryEAT{
		buckets: make(map[string]*InMemoryArcSet),
	}
}

// bucketKey generates a storage key for a root.
func bucketKey(c cid.Cid) string {
	return c.String()
}

// Get retrieves the target CID for (root, path).
func (e *BucketedInMemoryEAT) Get(root cid.Cid, path string) (cid.Cid, error) {
	e.mu.RLock()
	bucket, ok := e.buckets[bucketKey(root)]
	e.mu.RUnlock()

	if !ok {
		return cid.Cid{}, eat.ErrNotFound
	}

	target, ok := bucket.Get(path)
	if !ok {
		return cid.Cid{}, eat.ErrNotFound
	}
	return target, nil
}

// Put stores an arc entry, creating the bucket if needed.
func (e *BucketedInMemoryEAT) Put(root cid.Cid, path string, target cid.Cid) error {
	e.mu.Lock()
	key := bucketKey(root)
	bucket, ok := e.buckets[key]
	if !ok {
		bucket = NewInMemoryArcSet()
		e.buckets[key] = bucket
	}
	e.mu.Unlock()

	bucket.Set(path, target)
	return nil
}

// Delete removes an arc entry.
func (e *BucketedInMemoryEAT) Delete(root cid.Cid, path string) error {
	e.mu.RLock()
	bucket, ok := e.buckets[bucketKey(root)]
	e.mu.RUnlock()

	if !ok {
		return eat.ErrNotFound
	}

	bucket.Delete(path)
	return nil
}

// View returns an ArcSetView for a specific root (bucket).
func (e *BucketedInMemoryEAT) View(root cid.Cid) arcset.View {
	e.mu.RLock()
	bucket, ok := e.buckets[bucketKey(root)]
	e.mu.RUnlock()

	if !ok {
		// Return empty view for non-existent bucket
		return NewInMemoryArcSet()
	}
	return bucket
}

// Close releases resources.
func (e *BucketedInMemoryEAT) Close() error {
	e.mu.Lock()
	e.buckets = nil
	e.mu.Unlock()
	return nil
}

// Ensure BucketedInMemoryEAT implements eat.EAT.
var _ eat.EAT = (*BucketedInMemoryEAT)(nil)