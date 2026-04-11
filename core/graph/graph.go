// Package graph provides graph lifecycle management for MALT.
// A graph represents a scoped collection of arcs authenticated by structure commitments.
// Each graph has metadata (root CID, arc count, creation time) and a lifecycle state.
package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dewebprotocol/malt/core/kvstore"
	cid "github.com/ipfs/go-cid"
)

// Graph state constants.
const (
	StateActive   GraphState = "active"
	StateFrozen   GraphState = "frozen"
	StateDeleted  GraphState = "deleted"
)

// GraphState represents the lifecycle state of a graph.
type GraphState string

// Sentinel errors.
var (
	ErrNotFound      = errors.New("graph not found")
	ErrAlreadyExists = errors.New("graph already exists")
	ErrDeleted       = errors.New("graph is deleted")
	ErrFrozen        = errors.New("graph is frozen")
	ErrInvalidState  = errors.New("invalid graph state")
)

// Graph represents a MALT graph with metadata.
type Graph struct {
	ID          string     `json:"id"`
	Root        cid.Cid    `json:"root"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ArcCount    int        `json:"arc_count"`
	Backend     string     `json:"backend"`
	EATType     string     `json:"eat_type"`
	State       GraphState `json:"state"`
}

// IsActive returns true if the graph is in active state.
func (g *Graph) IsActive() bool {
	return g.State == StateActive
}

// IsFrozen returns true if the graph is frozen.
func (g *Graph) IsFrozen() bool {
	return g.State == StateFrozen
}

// IsDeleted returns true if the graph is deleted.
func (g *Graph) IsDeleted() bool {
	return g.State == StateDeleted
}

// UpdateRoot updates the root CID and arc count, recording the update time.
func (g *Graph) UpdateRoot(root cid.Cid, arcCount int) {
	g.Root = root
	g.ArcCount = arcCount
	g.UpdatedAt = time.Now()
}

// graphKey returns the KVStore key for a graph by ID.
func graphKey(id string) []byte {
	return []byte("graph/meta/" + id)
}

// graphIndexKey returns the KVStore key for the graph index entry.
func graphIndexKey(id string) []byte {
	return []byte("graph/index/" + id)
}

// graphIndexPrefix is the prefix for all graph index entries.
var graphIndexPrefix = []byte("graph/index/")

// Store persists and retrieves graph metadata.
type Store struct {
	kv kvstore.KVStore
}

// NewStore creates a new graph metadata store backed by a KVStore.
func NewStore(kv kvstore.KVStore) *Store {
	return &Store{kv: kv}
}

// Create persists a new graph entry.
func (s *Store) Create(ctx context.Context, g *Graph) error {
	exists, err := s.kv.Has(ctx, graphKey(g.ID))
	if err != nil {
		return fmt.Errorf("check existence: %w", err)
	}
	if exists {
		return ErrAlreadyExists
	}

	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("marshal graph: %w", err)
	}

	batch := s.kv.Batch()
	if err := batch.Put(graphKey(g.ID), data); err != nil {
		batch.Cancel()
		return fmt.Errorf("put graph: %w", err)
	}
	if err := batch.Put(graphIndexKey(g.ID), []byte(g.State)); err != nil {
		batch.Cancel()
		return fmt.Errorf("put index: %w", err)
	}
	return batch.Commit(ctx)
}

// Get retrieves a graph by ID.
func (s *Store) Get(ctx context.Context, id string) (*Graph, error) {
	data, err := s.kv.Get(ctx, graphKey(id))
	if err != nil {
		if errors.Is(err, kvstore.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get graph: %w", err)
	}

	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("unmarshal graph: %w", err)
	}
	return &g, nil
}

// Update persists updated graph metadata.
func (s *Store) Update(ctx context.Context, g *Graph) error {
	// Verify it exists
	exists, err := s.kv.Has(ctx, graphKey(g.ID))
	if err != nil {
		return fmt.Errorf("check existence: %w", err)
	}
	if !exists {
		return ErrNotFound
	}

	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("marshal graph: %w", err)
	}

	batch := s.kv.Batch()
	if err := batch.Put(graphKey(g.ID), data); err != nil {
		batch.Cancel()
		return fmt.Errorf("put graph: %w", err)
	}
	if err := batch.Put(graphIndexKey(g.ID), []byte(g.State)); err != nil {
		batch.Cancel()
		return fmt.Errorf("put index: %w", err)
	}
	return batch.Commit(ctx)
}

// Delete removes a graph entry (soft delete: sets state to deleted).
func (s *Store) Delete(ctx context.Context, id string) error {
	g, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if g.IsDeleted() {
		return ErrDeleted
	}

	g.State = StateDeleted
	g.UpdatedAt = time.Now()
	return s.Update(ctx, g)
}

// List returns all non-deleted graphs.
func (s *Store) List(ctx context.Context) ([]*Graph, error) {
	iter := s.kv.NewIterator(ctx, graphIndexPrefix, nil)
	defer iter.Close()

	var graphs []*Graph
	for iter.Next() {
		key := string(iter.Key())
		// Extract ID from "graph/index/<id>"
		id := key[len("graph/index/"):]
		if id == "" {
			continue
		}

		g, err := s.Get(ctx, id)
		if err != nil {
			continue // skip errors in individual reads
		}
		if g.IsDeleted() {
			continue
		}
		graphs = append(graphs, g)
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("iterate: %w", err)
	}
	return graphs, nil
}

// Manager manages graph lifecycle: creation, access, state transitions.
// It coordinates graph metadata with EAT/SCE buckets.
type Manager struct {
	store   *Store
	mu      sync.RWMutex
	graphs  map[string]*Graph // in-memory cache
}

// NewManager creates a new graph manager.
func NewManager(store *Store) *Manager {
	return &Manager{
		store:  store,
		graphs: make(map[string]*Graph),
	}
}

// CreateGraph creates a new graph with the given ID and initial parameters.
// The graph starts in the "active" state with an undefined root.
func (m *Manager) CreateGraph(ctx context.Context, id string, backend string, eatType string) (*Graph, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check in-memory cache first
	if _, ok := m.graphs[id]; ok {
		return nil, ErrAlreadyExists
	}

	// Check persistent store
	_, err := m.store.Get(ctx, id)
	if err == nil {
		return nil, ErrAlreadyExists
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("check graph existence: %w", err)
	}

	now := time.Now()
	g := &Graph{
		ID:        id,
		Root:      cid.Undef,
		CreatedAt: now,
		UpdatedAt: now,
		ArcCount:  0,
		Backend:   backend,
		EATType:   eatType,
		State:     StateActive,
	}

	if err := m.store.Create(ctx, g); err != nil {
		return nil, err
	}

	m.graphs[id] = g
	return g, nil
}

// GetGraph retrieves a graph by ID.
// Returns ErrNotFound if the graph doesn't exist, ErrDeleted if it's deleted.
func (m *Manager) GetGraph(ctx context.Context, id string) (*Graph, error) {
	m.mu.RLock()
	if g, ok := m.graphs[id]; ok {
		m.mu.RUnlock()
		if g.IsDeleted() {
			return nil, ErrDeleted
		}
		return g, nil
	}
	m.mu.RUnlock()

	g, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if g.IsDeleted() {
		return nil, ErrDeleted
	}

	// Cache it
	m.mu.Lock()
	m.graphs[id] = g
	m.mu.Unlock()

	return g, nil
}

// UpdateGraph updates a graph's root and arc count after a mutation.
// Only active graphs can be updated.
func (m *Manager) UpdateGraph(ctx context.Context, id string, root cid.Cid, arcCount int) (*Graph, error) {
	m.mu.Lock()
	g, ok := m.graphs[id]
	m.mu.Unlock()

	if !ok {
		var err error
		g, err = m.store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.graphs[id] = g
		m.mu.Unlock()
	}

	if !g.IsActive() {
		if g.IsFrozen() {
			return nil, ErrFrozen
		}
		return nil, ErrDeleted
	}

	g.UpdateRoot(root, arcCount)

	if err := m.store.Update(ctx, g); err != nil {
		return nil, err
	}
	return g, nil
}

// FreezeGraph transitions a graph to the "frozen" state.
// Frozen graphs cannot be updated but can still be read.
func (m *Manager) FreezeGraph(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	g, ok := m.graphs[id]
	if !ok {
		var err error
		g, err = m.store.Get(ctx, id)
		if err != nil {
			return err
		}
	}

	if !g.IsActive() {
		return fmt.Errorf("cannot freeze: graph is %s: %w", g.State, ErrInvalidState)
	}

	g.State = StateFrozen
	g.UpdatedAt = time.Now()

	if err := m.store.Update(ctx, g); err != nil {
		return err
	}

	m.graphs[id] = g

	return nil
}

// DeleteGraph soft-deletes a graph (sets state to "deleted").
func (m *Manager) DeleteGraph(ctx context.Context, id string) error {
	m.mu.Lock()
	delete(m.graphs, id)
	m.mu.Unlock()

	return m.store.Delete(ctx, id)
}

// ListGraphs returns all active and frozen graphs.
func (m *Manager) ListGraphs(ctx context.Context) ([]*Graph, error) {
	return m.store.List(ctx)
}

// RequireActive returns ErrFrozen or ErrDeleted if the graph is not active.
// This is a convenience helper for write-side validation.
func (m *Manager) RequireActive(ctx context.Context, id string) (*Graph, error) {
	g, err := m.GetGraph(ctx, id)
	if err != nil {
		return nil, err
	}
	if !g.IsActive() {
		return nil, fmt.Errorf("graph %q is %s: %w", id, g.State, ErrInvalidState)
	}
	return g, nil
}
