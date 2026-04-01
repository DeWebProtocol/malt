// Package simple provides a non-versioned EAT implementation.
package simple

import (
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat"
	cid "github.com/ipfs/go-cid"
)

// EAT is a non-versioned EAT implementation.
// It stores arcs directly: (root, path) -> target.
type EAT struct {
	mu   sync.RWMutex
	arcs map[string]map[string]cid.Cid // root -> path -> target
}

// NewEAT creates a new SimpleEAT.
func NewEAT() *EAT {
	return &EAT{
		arcs: make(map[string]map[string]cid.Cid),
	}
}

// rootKey generates a storage key for a root.
func rootKey(c cid.Cid) string {
	return c.String()
}

// Get retrieves the target CID for (root, path).
func (e *EAT) Get(root cid.Cid, path string) (cid.Cid, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rootStr := rootKey(root)
	paths, ok := e.arcs[rootStr]
	if !ok {
		return cid.Cid{}, eat.ErrNotFound
	}

	target, ok := paths[path]
	if !ok {
		return cid.Cid{}, eat.ErrNotFound
	}

	return target, nil
}

// Put stores an arc entry.
func (e *EAT) Put(root cid.Cid, path string, target cid.Cid) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rootStr := rootKey(root)
	if _, ok := e.arcs[rootStr]; !ok {
		e.arcs[rootStr] = make(map[string]cid.Cid)
	}

	e.arcs[rootStr][path] = target
	return nil
}

// Delete removes an arc entry.
func (e *EAT) Delete(root cid.Cid, path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rootStr := rootKey(root)
	paths, ok := e.arcs[rootStr]
	if !ok {
		return eat.ErrNotFound
	}

	if _, ok := paths[path]; !ok {
		return eat.ErrNotFound
	}

	delete(paths, path)
	return nil
}

// View returns an ArcSetView for a specific root.
func (e *EAT) View(root cid.Cid) arcset.View {
	return &eatView{eat: e, root: root}
}

// Close releases resources.
func (e *EAT) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.arcs = nil
	return nil
}

// eatView implements ArcSetView for SimpleEAT.
type eatView struct {
	eat  *EAT
	root cid.Cid
}

// Get retrieves the target CID for a path.
func (v *eatView) Get(path string) (cid.Cid, bool) {
	c, err := v.eat.Get(v.root, path)
	if err != nil {
		return cid.Cid{}, false
	}
	return c, true
}

// Iterate returns an iterator over all arcs for the root.
func (v *eatView) Iterate() arcset.Iterator {
	v.eat.mu.RLock()
	defer v.eat.mu.RUnlock()

	rootStr := rootKey(v.root)
	paths := v.eat.arcs[rootStr]

	// Copy paths to slice for iteration
	var arcs []struct {
		path string
		cid  cid.Cid
	}
	for p, c := range paths {
		arcs = append(arcs, struct {
			path string
			cid  cid.Cid
		}{path: p, cid: c})
	}

	// Sort by path
	sort.Slice(arcs, func(i, j int) bool {
		return arcs[i].path < arcs[j].path
	})

	return &eatIterator{arcs: arcs, idx: -1}
}

// Len returns the number of arcs.
func (v *eatView) Len() int {
	v.eat.mu.RLock()
	defer v.eat.mu.RUnlock()

	rootStr := rootKey(v.root)
	return len(v.eat.arcs[rootStr])
}

// eatIterator implements Iterator.
type eatIterator struct {
	arcs []struct {
		path string
		cid  cid.Cid
	}
	idx int
	err error
}

// Next advances to the next arc.
func (it *eatIterator) Next() (string, cid.Cid, bool) {
	it.idx++
	if it.idx >= len(it.arcs) {
		return "", cid.Cid{}, false
	}
	return it.arcs[it.idx].path, it.arcs[it.idx].cid, true
}

// Err returns any error encountered.
func (it *eatIterator) Err() error {
	return it.err
}

// Ensure EAT implements eat.EAT.
var _ eat.EAT = (*EAT)(nil)