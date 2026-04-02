// Package memory provides in-memory EAT implementations.
package memory

import (
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat"
	cid "github.com/ipfs/go-cid"
)

// MemoryView is a single-root in-memory arc set.
// It implements arcset.View for use with SCE commitment operations.
type MemoryView struct {
	mu   sync.RWMutex
	arcs map[string]cid.Cid
}

// NewView creates a new MemoryView.
func NewView() *MemoryView {
	return &MemoryView{
		arcs: make(map[string]cid.Cid),
	}
}

// Add adds an arc to the view.
func (v *MemoryView) Add(path string, c cid.Cid) {
	v.mu.Lock()
	v.arcs[path] = c
	v.mu.Unlock()
}

// Get retrieves the target CID for a path.
func (v *MemoryView) Get(path string) (cid.Cid, bool) {
	v.mu.RLock()
	c, ok := v.arcs[path]
	v.mu.RUnlock()
	return c, ok
}

// Iterate returns an iterator over all arcs.
func (v *MemoryView) Iterate() arcset.Iterator {
	v.mu.RLock()
	paths := make([]string, 0, len(v.arcs))
	for p := range v.arcs {
		paths = append(paths, p)
	}
	v.mu.RUnlock()

	sort.Strings(paths)
	return &viewIterator{v: v, paths: paths, idx: -1}
}

// Len returns the number of arcs.
func (v *MemoryView) Len() int {
	v.mu.RLock()
	n := len(v.arcs)
	v.mu.RUnlock()
	return n
}

// viewIterator implements arcset.Iterator.
type viewIterator struct {
	v     *MemoryView
	paths []string
	idx   int
}

// Next advances to the next arc.
func (it *viewIterator) Next() (string, cid.Cid, bool) {
	it.idx++
	if it.idx >= len(it.paths) {
		return "", cid.Cid{}, false
	}
	path := it.paths[it.idx]
	c, _ := it.v.Get(path)
	return path, c, true
}

// Err returns any error.
func (it *viewIterator) Err() error {
	return nil
}

// Ensure MemoryView implements arcset.View.
var _ arcset.View = (*MemoryView)(nil)

// MemoryEAT is a multi-root in-memory EAT implementation.
// It stores arcs per root: (root, path) -> target.
type MemoryEAT struct {
	mu   sync.RWMutex
	arcs map[string]map[string]cid.Cid // root -> path -> target
}

// NewEAT creates a new MemoryEAT.
func NewEAT() *MemoryEAT {
	return &MemoryEAT{
		arcs: make(map[string]map[string]cid.Cid),
	}
}

// rootKey generates a storage key for a root.
func rootKey(c cid.Cid) string {
	return c.String()
}

// Get retrieves the target CID for (root, path).
func (e *MemoryEAT) Get(root cid.Cid, path string) (cid.Cid, error) {
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
func (e *MemoryEAT) Put(root cid.Cid, path string, target cid.Cid) error {
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
func (e *MemoryEAT) Delete(root cid.Cid, path string) error {
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
func (e *MemoryEAT) View(root cid.Cid) arcset.View {
	return &eatView{eat: e, root: root}
}

// Close releases resources.
func (e *MemoryEAT) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.arcs = nil
	return nil
}

// eatView implements arcset.View for MemoryEAT.
type eatView struct {
	eat  *MemoryEAT
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

// eatIterator implements arcset.Iterator.
type eatIterator struct {
	arcs []struct {
		path string
		cid  cid.Cid
	}
	idx int
}

// Next advances to the next arc.
func (it *eatIterator) Next() (string, cid.Cid, bool) {
	it.idx++
	if it.idx >= len(it.arcs) {
		return "", cid.Cid{}, false
	}
	return it.arcs[it.idx].path, it.arcs[it.idx].cid, true
}

// Err returns any error.
func (it *eatIterator) Err() error {
	return nil
}

// Ensure MemoryEAT implements eat.EAT.
var _ eat.EAT = (*MemoryEAT)(nil)