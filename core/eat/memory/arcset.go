// Package memory provides in-memory implementations for EAT and arc sets.
package memory

import (
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
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