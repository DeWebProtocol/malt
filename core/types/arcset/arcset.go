// Package arcset defines interfaces for arc set views.
// An arc set is a collection of path -> CID mappings.
package arcset

import (
	"errors"

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

// View is an alias for Snapshot for backward compatibility.
// Deprecated: Use Snapshot instead.
type View = Snapshot

// Map is a simple implementation of View backed by a map.
// It is not thread-safe and intended for building temporary arc sets.
type Map struct {
	arcs map[string]cid.Cid
}

// NewMap creates a new Map.
func NewMap() *Map {
	return &Map{arcs: make(map[string]cid.Cid)}
}

// NewMapFrom creates a new Map from an existing map.
func NewMapFrom(arcs map[string]cid.Cid) *Map {
	return &Map{arcs: arcs}
}

// Set adds or updates an arc.
func (m *Map) Set(path string, target cid.Cid) {
	m.arcs[path] = target
}

// Delete removes an arc.
func (m *Map) Delete(path string) {
	delete(m.arcs, path)
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

// AsMap returns the underlying map.
func (m *Map) AsMap() map[string]cid.Cid {
	return m.arcs
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
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
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