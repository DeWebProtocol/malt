// Package eat defines the Explicit Arc Table interface and implementations.
package eat

import (
	"sync"

	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
)

// SimpleEAT is a non-versioned EAT implementation.
// It stores arcs directly: (root, path) -> target.
type SimpleEAT struct {
	mu    sync.RWMutex
	arcs  map[string]map[string]key.Key // root -> path -> target
}

// NewSimpleEAT creates a new SimpleEAT.
func NewSimpleEAT() *SimpleEAT {
	return &SimpleEAT{
		arcs: make(map[string]map[string]key.Key),
	}
}

// rootKey generates a storage key for a root.
func rootKey(k key.Key) string {
	return k.String()
}

// Get retrieves the target key for (root, path).
func (e *SimpleEAT) Get(root key.Key, path string) (key.Key, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	rootStr := rootKey(root)
	paths, ok := e.arcs[rootStr]
	if !ok {
		return nil, ErrNotFound
	}

	target, ok := paths[path]
	if !ok {
		return nil, ErrNotFound
	}

	return target, nil
}

// Put stores an arc entry.
func (e *SimpleEAT) Put(root key.Key, path string, target key.Key) error {
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
func (e *SimpleEAT) Delete(root key.Key, path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	rootStr := rootKey(root)
	paths, ok := e.arcs[rootStr]
	if !ok {
		return ErrNotFound
	}

	if _, ok := paths[path]; !ok {
		return ErrNotFound
	}

	delete(paths, path)
	return nil
}

// View returns an ArcSetView for a specific root.
func (e *SimpleEAT) View(root key.Key) sce.ArcSetView {
	return &simpleEATView{eat: e, root: root}
}

// Close releases resources.
func (e *SimpleEAT) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.arcs = nil
	return nil
}

// simpleEATView implements ArcSetView for SimpleEAT.
type simpleEATView struct {
	eat  *SimpleEAT
	root key.Key
}

// Get retrieves the target key for a path.
func (v *simpleEATView) Get(path string) (key.Key, bool) {
	k, err := v.eat.Get(v.root, path)
	if err != nil {
		return nil, false
	}
	return k, true
}

// Iterate returns an iterator over all arcs for the root.
func (v *simpleEATView) Iterate() sce.ArcIterator {
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

	return &simpleEATIterator{arcs: arcs, idx: -1}
}

// Len returns the number of arcs.
func (v *simpleEATView) Len() int {
	v.eat.mu.RLock()
	defer v.eat.mu.RUnlock()

	rootStr := rootKey(v.root)
	return len(v.eat.arcs[rootStr])
}

// simpleEATIterator implements ArcIterator.
type simpleEATIterator struct {
	arcs []struct {
		path string
		key  key.Key
	}
	idx int
	err error
}

// Next advances to the next arc.
func (it *simpleEATIterator) Next() (string, key.Key, bool) {
	it.idx++
	if it.idx >= len(it.arcs) {
		return "", nil, false
	}
	return it.arcs[it.idx].path, it.arcs[it.idx].key, true
}

// Err returns any error encountered.
func (it *simpleEATIterator) Err() error {
	return it.err
}