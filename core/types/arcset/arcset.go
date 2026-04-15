// Package arcset defines interfaces for arc set views.
// An arc set is a collection of path -> CID mappings.
package arcset

import (
	"errors"
	"slices"

	cid "github.com/ipfs/go-cid"
)

// ErrNotFound is returned when an arc is not found.
var ErrNotFound = errors.New("arc not found")

// Iterator iterates over arcs in an arc set.
type Iterator interface {
	// Next advances to the next arc.
	// Returns (path, cid, true) if there is an arc, or (_, _, false) if done.
	Next() (path string, c cid.Cid, ok bool)

	// Err returns any error encountered during iteration.
	Err() error

	// Close releases iterator resources.
	// Must be called when iteration is complete.
	Close()
}

// Snapshot provides an immutable snapshot of an arc set.
// It preloads all data into memory, suitable for random access
// and multiple iterations.
type Snapshot interface {
	// Get retrieves the target CID for a path.
	Get(path string) (cid.Cid, bool)

	// Iterate returns an iterator over all arcs.
	Iterate() Iterator

	// Len returns the number of arcs.
	Len() int
}

// Map is an immutable snapshot backed by a map.
// It is safe for concurrent read access.
// Do not modify the underlying map after creation.
type Map struct {
	arcs map[string]cid.Cid
}

// NewMap creates an empty Map.
func NewMap() *Map {
	return &Map{arcs: make(map[string]cid.Cid)}
}

// NewMapFrom creates a Map from an existing map.
// The map is used directly without copying; caller should not modify it afterwards.
func NewMapFrom(arcs map[string]cid.Cid) *Map {
	return &Map{arcs: arcs}
}

// Get retrieves the target CID for a path.
func (m *Map) Get(path string) (cid.Cid, bool) {
	c, ok := m.arcs[path]
	return c, ok
}

// Iterate returns an iterator over all arcs.
func (m *Map) Iterate() Iterator {
	return &mapIterator{arcs: m.arcs, keys: getSortedKeys(m.arcs)}
}

// Len returns the number of arcs.
func (m *Map) Len() int {
	return len(m.arcs)
}

// mapIterator implements Iterator for Map.
type mapIterator struct {
	arcs   map[string]cid.Cid
	keys   []string
	index  int
}

func getSortedKeys(m map[string]cid.Cid) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Sort for deterministic iteration
	slices.Sort(keys)
	return keys
}

func (it *mapIterator) Next() (string, cid.Cid, bool) {
	if it.index >= len(it.keys) {
		return "", cid.Cid{}, false
	}
	key := it.keys[it.index]
	it.index++
	return key, it.arcs[key], true
}

func (it *mapIterator) Err() error {
	return nil
}

func (it *mapIterator) Close() {
	// No resources to release
}

// Ensure Map implements Snapshot.
var _ Snapshot = (*Map)(nil)