package arcset

import (
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/key"
)

// Map provides a simple in-memory ArcSetView implementation.
type Map struct {
	mu   sync.RWMutex
	arcs map[string]key.Key
}

// NewMap creates a new Map.
func NewMap() *Map {
	return &Map{
		arcs: make(map[string]key.Key),
	}
}

// Add adds an arc to the map.
func (m *Map) Add(path string, k key.Key) {
	m.mu.Lock()
	m.arcs[path] = k
	m.mu.Unlock()
}

// Get retrieves the target key for a path.
func (m *Map) Get(path string) (key.Key, bool) {
	m.mu.RLock()
	k, ok := m.arcs[path]
	m.mu.RUnlock()
	return k, ok
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
func (it *mapIterator) Next() (string, key.Key, bool) {
	it.idx++
	if it.idx >= len(it.paths) {
		return "", nil, false
	}
	path := it.paths[it.idx]
	k, _ := it.m.Get(path)
	return path, k, true
}

// Err returns any error.
func (it *mapIterator) Err() error {
	return it.err
}