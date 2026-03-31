// Package simple provides a non-versioned EAT implementation.
package simple

import (
	"sync"

	"github.com/dewebprotocol/malt/internal/eat"
	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
)

// EAT is a non-versioned EAT implementation.
// It stores arcs directly: (root, path) -> target.
type EAT struct {
	mu   sync.RWMutex
	arcs map[string]map[string]key.Key // root -> path -> target
}

// NewEAT creates a new SimpleEAT.
func NewEAT() *EAT {
	return &EAT{
		arcs: make(map[string]map[string]key.Key),
	}
}

// rootKey generates a storage key for a root.
func rootKey(k key.Key) string {
	return k.String()
}

// Get retrieves the target key for (root, path).
func (e *EAT) Get(root key.Key, path string) (key.Key, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rootStr := rootKey(root)
	paths, ok := e.arcs[rootStr]
	if !ok {
		return nil, eat.ErrNotFound
	}

	target, ok := paths[path]
	if !ok {
		return nil, eat.ErrNotFound
	}

	return target, nil
}

// Put stores an arc entry.
func (e *EAT) Put(root key.Key, path string, target key.Key) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rootStr := rootKey(root)
	if _, ok := e.arcs[rootStr]; !ok {
		e.arcs[rootStr] = make(map[string]key.Key)
	}

	e.arcs[rootStr][path] = target
	return nil
}

// Delete removes an arc entry.
func (e *EAT) Delete(root key.Key, path string) error {
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
func (e *EAT) View(root key.Key) sce.ArcSetView {
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
	root key.Key
}

// Get retrieves the target key for a path.
func (v *eatView) Get(path string) (key.Key, bool) {
	k, err := v.eat.Get(v.root, path)
	if err != nil {
		return nil, false
	}
	return k, true
}

// Iterate returns an iterator over all arcs for the root.
func (v *eatView) Iterate() sce.ArcIterator {
	v.eat.mu.RLock()
	defer v.eat.mu.RUnlock()

	rootStr := rootKey(v.root)
	paths := v.eat.arcs[rootStr]

	// Copy paths to slice for iteration
	var arcs []struct {
		path string
		key  key.Key
	}
	for p, k := range paths {
		arcs = append(arcs, struct {
			path string
			key  key.Key
		}{path: p, key: k})
	}

	return &eatIterator{arcs: arcs, idx: -1}
}

// Len returns the number of arcs.
func (v *eatView) Len() int {
	v.eat.mu.RLock()
	defer v.eat.mu.RUnlock()

	rootStr := rootKey(v.root)
	return len(v.eat.arcs[rootStr])
}

// eatIterator implements ArcIterator.
type eatIterator struct {
	arcs []struct {
		path string
		key  key.Key
	}
	idx int
	err error
}

// Next advances to the next arc.
func (it *eatIterator) Next() (string, key.Key, bool) {
	it.idx++
	if it.idx >= len(it.arcs) {
		return "", nil, false
	}
	return it.arcs[it.idx].path, it.arcs[it.idx].key, true
}

// Err returns any error encountered.
func (it *eatIterator) Err() error {
	return it.err
}

// Ensure EAT implements eat.EAT.
var _ eat.EAT = (*EAT)(nil)