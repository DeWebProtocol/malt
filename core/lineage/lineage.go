// Package lineage provides version lineage tracking for MALT structures.
// It records parent-child relationships between structure roots,
// enabling ancestry traversal, depth tracking, and copy-on-write optimizations.
//
// The lineage module coordinates with the versioned EAT's @previous chain:
// the EAT stores @previous as an arc within each version, while the lineage
// module maintains an independent index for fast lookups without EAT resolution.
package lineage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	cid "github.com/ipfs/go-cid"
)

// KVStore is the interface used by the lineage store for persistence.
type KVStore interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte) error
	Delete(key string) error
	Keys() []string
}

// LineageRecord stores metadata about a single structure version.
type LineageRecord struct {
	Root      cid.Cid    `json:"root"`
	Parent    cid.Cid    `json:"parent"`    // cid.Undef for root versions
	Timestamp time.Time  `json:"timestamp"`
	Depth     int        `json:"depth"`     // distance from the root version
	ArcCount  int        `json:"arc_count"` // number of arcs in this version
}

// MarshalJSON encodes a LineageRecord to JSON with CID strings.
func (r *LineageRecord) MarshalJSON() ([]byte, error) {
	rootStr := ""
	if r.Root.Defined() {
		rootStr = r.Root.String()
	}
	parentStr := ""
	if r.Parent.Defined() {
		parentStr = r.Parent.String()
	}
	type Alias LineageRecord
	return json.Marshal(&struct {
		Root   string `json:"root"`
		Parent string `json:"parent"`
		*Alias
	}{
		Root:   rootStr,
		Parent: parentStr,
		Alias:  (*Alias)(r),
	})
}

// UnmarshalJSON decodes a LineageRecord from JSON with CID strings.
func (r *LineageRecord) UnmarshalJSON(data []byte) error {
	var aux struct {
		Root      string    `json:"root"`
		Parent    string    `json:"parent"`
		Timestamp time.Time `json:"timestamp"`
		Depth     int       `json:"depth"`
		ArcCount  int       `json:"arc_count"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Root != "" {
		c, err := cid.Decode(aux.Root)
		if err != nil {
			return fmt.Errorf("invalid root CID: %w", err)
		}
		r.Root = c
	}
	if aux.Parent != "" {
		c, err := cid.Decode(aux.Parent)
		if err != nil {
			return fmt.Errorf("invalid parent CID: %w", err)
		}
		r.Parent = c
	}
	r.Timestamp = aux.Timestamp
	r.Depth = aux.Depth
	r.ArcCount = aux.ArcCount
	return nil
}

// Store provides lineage persistence backed by a KVStore.
// Keys:
//
//	"lineage:<root>"       -> LineageRecord for the root
//	"children:<parent>"    -> []cid.Cid (list of child roots)
type Store struct {
	kv KVStore
}

// NewStore creates a new lineage store backed by the given KVStore.
func NewStore(kv KVStore) *Store {
	return &Store{kv: kv}
}

// lineageKey returns the KV key for a lineage record.
func lineageKey(root string) string {
	return "lineage:" + root
}

// childrenKey returns the KV key for the children index.
func childrenKey(parent string) string {
	return "children:" + parent
}

// Record stores a lineage record and updates the parent's children index.
func (s *Store) Record(ctx context.Context, rec *LineageRecord) error {
	if !rec.Root.Defined() {
		return fmt.Errorf("root CID is undefined")
	}

	// Serialize the record
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal lineage record: %w", err)
	}

	// Store the record
	if err := s.kv.Set(lineageKey(rec.Root.String()), data); err != nil {
		return fmt.Errorf("store lineage record: %w", err)
	}

	// Update parent's children index
	if rec.Parent.Defined() {
		children, err := s.getChildren(rec.Parent.String())
		if err != nil {
			return err
		}
		// Add child if not already present
		found := false
		for _, c := range children {
			if c.Equals(rec.Root) {
				found = true
				break
			}
		}
		if !found {
			children = append(children, rec.Root)
			if err := s.setChildren(rec.Parent.String(), children); err != nil {
				return err
			}
		}
	}

	return nil
}

// Get retrieves a lineage record by root CID.
func (s *Store) Get(ctx context.Context, root cid.Cid) (*LineageRecord, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root CID is undefined")
	}

	data, ok := s.kv.Get(lineageKey(root.String()))
	if !ok {
		return nil, fmt.Errorf("lineage record not found for %s", root)
	}

	var rec LineageRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal lineage record: %w", err)
	}
	return &rec, nil
}

// Delete removes a lineage record and its entry in the parent's children index.
func (s *Store) Delete(ctx context.Context, root cid.Cid) error {
	if !root.Defined() {
		return fmt.Errorf("root CID is undefined")
	}

	rec, err := s.Get(ctx, root)
	if err != nil {
		return err
	}

	// Remove from parent's children index
	if rec.Parent.Defined() {
		children, err := s.getChildren(rec.Parent.String())
		if err == nil {
			newChildren := make([]cid.Cid, 0, len(children))
			for _, c := range children {
				if !c.Equals(root) {
					newChildren = append(newChildren, c)
				}
			}
			_ = s.setChildren(rec.Parent.String(), newChildren)
		}
	}

	return s.kv.Delete(lineageKey(root.String()))
}

// Ancestors returns the chain of ancestors from root to the oldest version.
// If maxDepth > 0, limits traversal to maxDepth steps.
func (s *Store) Ancestors(ctx context.Context, root cid.Cid, maxDepth int) ([]cid.Cid, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root CID is undefined")
	}

	var result []cid.Cid
	current := root
	depth := 0

	for {
		rec, err := s.Get(ctx, current)
		if err != nil {
			break
		}
		if !rec.Parent.Defined() {
			break
		}
		current = rec.Parent
		result = append(result, current)
		depth++
		if maxDepth > 0 && depth >= maxDepth {
			break
		}
	}

	return result, nil
}

// Descendants returns all known child versions of the given root (one level).
func (s *Store) Descendants(ctx context.Context, root cid.Cid) ([]cid.Cid, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root CID is undefined")
	}
	return s.getChildren(root.String())
}

// Depth returns the lineage depth of a version (distance from root).
func (s *Store) Depth(ctx context.Context, root cid.Cid) (int, error) {
	rec, err := s.Get(ctx, root)
	if err != nil {
		return 0, err
	}
	return rec.Depth, nil
}

// List returns all lineage records.
func (s *Store) List(ctx context.Context) ([]*LineageRecord, error) {
	var records []*LineageRecord
	for _, key := range s.kv.Keys() {
		if len(key) > 8 && key[:8] == "lineage:" {
			data, ok := s.kv.Get(key)
			if !ok {
				continue
			}
			var rec LineageRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				continue
			}
			records = append(records, &rec)
		}
	}
	// Sort by timestamp
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp.Before(records[j].Timestamp)
	})
	return records, nil
}

// Count returns the number of lineage records.
func (s *Store) Count(ctx context.Context) int {
	count := 0
	for _, key := range s.kv.Keys() {
		if len(key) > 8 && key[:8] == "lineage:" {
			count++
		}
	}
	return count
}

// getChildren returns the list of children for a parent root.
func (s *Store) getChildren(parent string) ([]cid.Cid, error) {
	data, ok := s.kv.Get(childrenKey(parent))
	if !ok {
		return nil, nil
	}
	var children []cid.Cid
	if err := json.Unmarshal(data, &children); err != nil {
		return nil, fmt.Errorf("unmarshal children: %w", err)
	}
	return children, nil
}

// setChildren stores the list of children for a parent root.
func (s *Store) setChildren(parent string, children []cid.Cid) error {
	data, err := json.Marshal(children)
	if err != nil {
		return fmt.Errorf("marshal children: %w", err)
	}
	return s.kv.Set(childrenKey(parent), data)
}

// Manager provides concurrent-safe access to lineage records with caching.
type Manager struct {
	mu     sync.RWMutex
	store  *Store
	cache  map[string]*LineageRecord // root string -> record
}

// NewManager creates a new lineage manager.
func NewManager(store *Store) *Manager {
	return &Manager{
		store: store,
		cache: make(map[string]*LineageRecord),
	}
}

// Record records a new lineage entry.
func (m *Manager) Record(ctx context.Context, root cid.Cid, parent cid.Cid, arcCount int) error {
	// Calculate depth
	depth := 1
	if parent.Defined() {
		parentRec, err := m.Get(ctx, parent)
		if err == nil {
			depth = parentRec.Depth + 1
		}
	}

	rec := &LineageRecord{
		Root:      root,
		Parent:    parent,
		Timestamp: time.Now(),
		Depth:     depth,
		ArcCount:  arcCount,
	}

	if err := m.store.Record(ctx, rec); err != nil {
		return err
	}

	m.mu.Lock()
	m.cache[root.String()] = rec
	m.mu.Unlock()

	return nil
}

// Get retrieves a lineage record.
func (m *Manager) Get(ctx context.Context, root cid.Cid) (*LineageRecord, error) {
	rootStr := root.String()

	m.mu.RLock()
	if rec, ok := m.cache[rootStr]; ok {
		m.mu.RUnlock()
		return rec, nil
	}
	m.mu.RUnlock()

	rec, err := m.store.Get(ctx, root)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cache[rootStr] = rec
	m.mu.Unlock()

	return rec, nil
}

// Delete removes a lineage record.
func (m *Manager) Delete(ctx context.Context, root cid.Cid) error {
	if err := m.store.Delete(ctx, root); err != nil {
		return err
	}

	m.mu.Lock()
	delete(m.cache, root.String())
	m.mu.Unlock()

	return nil
}

// Ancestors returns the ancestor chain.
func (m *Manager) Ancestors(ctx context.Context, root cid.Cid, maxDepth int) ([]cid.Cid, error) {
	return m.store.Ancestors(ctx, root, maxDepth)
}

// Descendants returns direct children.
func (m *Manager) Descendants(ctx context.Context, root cid.Cid) ([]cid.Cid, error) {
	return m.store.Descendants(ctx, root)
}

// Depth returns the version depth.
func (m *Manager) Depth(ctx context.Context, root cid.Cid) (int, error) {
	rec, err := m.Get(ctx, root)
	if err != nil {
		return 0, err
	}
	return rec.Depth, nil
}

// List returns all lineage records.
func (m *Manager) List(ctx context.Context) ([]*LineageRecord, error) {
	return m.store.List(ctx)
}

// Count returns the number of records.
func (m *Manager) Count(ctx context.Context) int {
	return m.store.Count(ctx)
}
