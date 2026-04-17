package arcset

import (
	"slices"

	cid "github.com/ipfs/go-cid"
)

// Set is an immutable in-memory ArcSet implementation.
// It is safe for concurrent read access.
// Do not modify the underlying map after creation.
type Set struct {
	arcs map[Path]cid.Cid
}

// NewSet creates an empty in-memory arc set.
func NewSet() *Set {
	return &Set{arcs: make(map[Path]cid.Cid)}
}

// NewSetFrom creates an in-memory arc set from path strings.
// Input paths are canonicalized before insertion.
func NewSetFrom(arcs map[string]cid.Cid) *Set {
	out := make(map[Path]cid.Cid, len(arcs))
	for path, target := range arcs {
		out[CanonicalizePath(path)] = target
	}
	return &Set{arcs: out}
}

// NewSetFromPaths creates an in-memory arc set from canonical paths.
func NewSetFromPaths(arcs map[Path]cid.Cid) *Set {
	return &Set{arcs: arcs}
}

// Get retrieves the target CID for a path.
func (m *Set) Get(path Path) (cid.Cid, bool) {
	c, ok := m.arcs[path]
	return c, ok
}

// Iterate returns an iterator over all arcs.
func (m *Set) Iterate() Iterator {
	return &memoryIterator{arcs: m.arcs, keys: getSortedKeys(m.arcs)}
}

// Len returns the number of arcs.
func (m *Set) Len() int {
	return len(m.arcs)
}

type memoryIterator struct {
	arcs  map[Path]cid.Cid
	keys  []Path
	index int
}

func getSortedKeys(m map[Path]cid.Cid) []Path {
	keys := make([]Path, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (it *memoryIterator) Next() (Path, cid.Cid, bool) {
	if it.index >= len(it.keys) {
		return "", cid.Cid{}, false
	}
	key := it.keys[it.index]
	it.index++
	return key, it.arcs[key], true
}

func (it *memoryIterator) Err() error { return nil }

func (it *memoryIterator) Close() {}

var _ ArcSet = (*Set)(nil)
