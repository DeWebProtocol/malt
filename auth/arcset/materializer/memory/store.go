// Package memory provides an in-memory ArcSet materializer for SDK examples,
// conformance vectors, and tests. It is not a persistent ArcTable and carries
// no deployment policy.
package memory

import (
	"context"
	"sync"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/arcset/materializer"
	cid "github.com/ipfs/go-cid"
)

type Store struct {
	mu        sync.RWMutex
	branching bool
	scopes    map[string]*scopeState
}

type scopeState struct {
	current map[arcset.Path]cid.Cid
	roots   map[string]map[arcset.Path]cid.Cid
}

// New creates an in-memory materializer. branching controls whether snapshots
// for multiple children of the same root are preserved.
func New(branching bool) *Store {
	return &Store{branching: branching, scopes: map[string]*scopeState{}}
}

func (s *Store) SupportsConcurrentBranches() bool { return s.branching }

func (s *Store) Get(_ context.Context, scope string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.scopes[scope]
	if state == nil {
		return cid.Undef, materializer.ErrNotFound
	}
	if root.Defined() {
		snapshot, ok := state.roots[root.KeyString()]
		if !ok {
			return cid.Undef, materializer.ErrNotFound
		}
		if !s.branching {
			snapshot = state.current
		}
		if target, ok := snapshot[path]; ok {
			return target, nil
		}
	}
	if target, ok := state.current[path]; ok {
		return target, nil
	}
	return cid.Undef, materializer.ErrNotFound
}

func (s *Store) BatchGet(ctx context.Context, scope string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	out := make(map[arcset.Path]cid.Cid, len(paths))
	for _, path := range paths {
		target, err := s.Get(ctx, scope, root, path)
		if err == nil {
			out[path] = target
			continue
		}
		if !materializer.IsNotFound(err) {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) Update(_ context.Context, scope string, newRoot, oldRoot cid.Cid, values arcset.ArcSet) error {
	delta, err := arcset.ToPathMap(values)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureScope(scope)
	if !newRoot.Defined() {
		apply(state.current, delta)
		return nil
	}

	base := map[arcset.Path]cid.Cid{}
	if !s.branching {
		base = clone(state.current)
	}
	if oldRoot.Defined() {
		previous, ok := state.roots[oldRoot.KeyString()]
		if !ok {
			if s.branching {
				return materializer.ErrNotFound
			}
			previous = state.current
		}
		if s.branching {
			base = clone(previous)
			for path, target := range state.current {
				base[path] = target
			}
		}
	} else if s.branching {
		base = clone(state.current)
	}
	apply(base, delta)
	if !s.branching {
		state.roots = map[string]map[arcset.Path]cid.Cid{}
		state.current = clone(base)
	}
	state.roots[newRoot.KeyString()] = base
	return nil
}

func (s *Store) Snapshot(_ context.Context, scope string, root cid.Cid) (arcset.ArcSet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.scopes[scope]
	if state == nil {
		return nil, materializer.ErrNotFound
	}
	values := state.current
	if root.Defined() {
		var ok bool
		values, ok = state.roots[root.KeyString()]
		if !ok {
			return nil, materializer.ErrNotFound
		}
		if !s.branching {
			values = state.current
		}
	}
	return arcset.NewArcSetFromPaths(clone(values))
}

func (s *Store) Iterate(ctx context.Context, scope string, root cid.Cid) arcset.Iterator {
	view, err := s.Snapshot(ctx, scope, root)
	if err != nil {
		return &errorIterator{err: err}
	}
	return view.Iterate()
}

func (s *Store) Close() error { return nil }

// DeleteRoot removes one materialized root. It exists for conformance and
// failure-injection tests; production persistence belongs to the caller.
func (s *Store) DeleteRoot(scope string, root cid.Cid) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.scopes[scope]; state != nil && root.Defined() {
		delete(state.roots, root.KeyString())
	}
}

// ReplaceRoot installs an explicit materialized ArcSet for conformance and
// failure-injection tests.
func (s *Store) ReplaceRoot(scope string, root cid.Cid, values map[arcset.Path]cid.Cid) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureScope(scope)
	state.roots[root.KeyString()] = clone(values)
	if !s.branching {
		state.current = clone(values)
	}
}

// SetCurrent changes one unversioned materialized coordinate. It is intended
// for integrity tests that simulate an untrusted or corrupted materializer.
func (s *Store) SetCurrent(scope string, path arcset.Path, target cid.Cid) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.ensureScope(scope)
	if target.Defined() {
		state.current[path] = target
	} else {
		delete(state.current, path)
	}
}

func (s *Store) ensureScope(scope string) *scopeState {
	state := s.scopes[scope]
	if state == nil {
		state = &scopeState{current: map[arcset.Path]cid.Cid{}, roots: map[string]map[arcset.Path]cid.Cid{}}
		s.scopes[scope] = state
	}
	return state
}

func apply(target, delta map[arcset.Path]cid.Cid) {
	for path, value := range delta {
		if value.Defined() {
			target[path] = value
		} else {
			delete(target, path)
		}
	}
}

func clone(values map[arcset.Path]cid.Cid) map[arcset.Path]cid.Cid {
	out := make(map[arcset.Path]cid.Cid, len(values))
	for path, value := range values {
		out[path] = value
	}
	return out
}

type errorIterator struct{ err error }

func (i *errorIterator) Next() (arcset.Path, cid.Cid, bool) { return "", cid.Undef, false }
func (i *errorIterator) Err() error                         { return i.err }
func (i *errorIterator) Close()                             {}

var (
	_ materializer.Store          = (*Store)(nil)
	_ materializer.BranchingStore = (*Store)(nil)
)
