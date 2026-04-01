package arcset

import (
	"sort"
	"sync"

	cid "github.com/ipfs/go-cid"
)

// Map provides a simple in-memory ArcSetView implementation.
type Map struct {
	mu   sync.RWMutex
	arcs map[string]cid.Cid
}

// NewMap creates a new Map.
func NewMap() *Map {
	return &Map{
		arcs: make(map[string]cid.Cid),
	}
}

// Add adds an arc to the map.
func (m *Map) Add(path string, c cid.Cid) {
	m.mu.Lock()
	m.arcs[path] = c
	m.mu.Unlock()
}

// Get retrieves the target CID for a path.
func (m *Map) Get(path string) (cid.Cid, bool) {
	m.mu.RLock()
	c, ok := m.arcs[path]
	m.mu.RUnlock()
	return c, ok
}

// Iterate returns an iterator.
func (m *Map) Iterate() Iterator {
	m.mu.RLock()
	paths := make([]string, 0, len(m.arcs))
	for p := range m.arcs {
		paths = append(paths, p)
	}
	m.mu.RUnlock()

	sort.Strings(paths)
	return &mapIterator{m: m, paths: paths, idx: -1}
}

// Len returns the number of arcs.
func (m *Map) Len() int {
	m.mu.RLock()
	n := len(m.arcs)
	m.mu.RUnlock()
	return n
}

// mapIterator implements Iterator.
type mapIterator struct {
	m     *Map
	paths []string
	idx   int
	err   error
}

// Next advances to the next arc.
func (it *mapIterator) Next() (string, cid.Cid, bool) {
	it.idx++
	if it.idx >= len(it.paths) {
		return "", cid.Cid{}, false
	}
	path := it.paths[it.idx]
	c, _ := it.m.Get(path)
	return path, c, true
}

// Err returns any error.
func (it *mapIterator) Err() error {
	return it.err
}