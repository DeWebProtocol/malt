// Package lineage provides version lineage tracking for MALT structures.
package lineage

import (
	"context"
	"encoding/json"
	"fmt"

	cid "github.com/ipfs/go-cid"
)

// ShortcutEntry stores a precomputed shortcut from a child to an ancestor
// at a specific depth distance. This accelerates lineage traversal by
// skipping intermediate versions.
type ShortcutEntry struct {
	From   cid.Cid `json:"from"`   // current version
	To     cid.Cid `json:"to"`     // ancestor version at distance
	Dist   int     `json:"dist"`   // distance (number of hops)
	Depth  int     `json:"depth"`  // depth of the ancestor
}

// MarshalJSON encodes a ShortcutEntry with CID strings.
func (s *ShortcutEntry) MarshalJSON() ([]byte, error) {
	type Alias ShortcutEntry
	fromStr := ""
	if s.From.Defined() {
		fromStr = s.From.String()
	}
	toStr := ""
	if s.To.Defined() {
		toStr = s.To.String()
	}
	return json.Marshal(&struct {
		From string `json:"from"`
		To   string `json:"to"`
		*Alias
	}{
		From:  fromStr,
		To:    toStr,
		Alias: (*Alias)(s),
	})
}

// UnmarshalJSON decodes a ShortcutEntry from CID strings.
func (s *ShortcutEntry) UnmarshalJSON(data []byte) error {
	var raw struct {
		From  string `json:"from"`
		To    string `json:"to"`
		Dist  int    `json:"dist"`
		Depth int    `json:"depth"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.From != "" {
		c, err := cid.Decode(raw.From)
		if err != nil {
			return fmt.Errorf("invalid from CID: %w", err)
		}
		s.From = c
	}
	if raw.To != "" {
		c, err := cid.Decode(raw.To)
		if err != nil {
			return fmt.Errorf("invalid to CID: %w", err)
		}
		s.To = c
	}
	s.Dist = raw.Dist
	s.Depth = raw.Depth
	return nil
}

// cowShortcutKey returns the KV key for a COW shortcut.
func cowShortcutKey(root string) string {
	return "cow:" + root
}

// RecordShortcut records a COW shortcut for a root.
// This is called when a new version is created to shortcut directly
// to the nearest ancestor that has the arc we're looking for.
func (s *Store) RecordShortcut(ctx context.Context, from cid.Cid, to cid.Cid, dist int) error {
	entry := ShortcutEntry{
		From:  from,
		To:    to,
		Dist:  dist,
	}

	data, err := json.Marshal(&entry)
	if err != nil {
		return fmt.Errorf("marshal shortcut: %w", err)
	}
	return s.kv.Set(cowShortcutKey(from.String()), data)
}

// GetShortcut retrieves a COW shortcut for a root.
// Returns nil if no shortcut exists.
func (s *Store) GetShortcut(ctx context.Context, root cid.Cid) (*ShortcutEntry, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root CID is undefined")
	}

	data, ok := s.kv.Get(cowShortcutKey(root.String()))
	if !ok {
		return nil, nil
	}

	var entry ShortcutEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal shortcut: %w", err)
	}
	return &entry, nil
}

// DeleteShortcut removes a COW shortcut.
func (s *Store) DeleteShortcut(ctx context.Context, root cid.Cid) error {
	if !root.Defined() {
		return fmt.Errorf("root CID is undefined")
	}
	return s.kv.Delete(cowShortcutKey(root.String()))
}

// ManagerCOW extends Manager with COW shortcut support.
type ManagerCOW struct {
	*Manager
}

// NewManagerCOW creates a COW-aware lineage manager.
func NewManagerCOW(store *Store) *ManagerCOW {
	return &ManagerCOW{Manager: NewManager(store)}
}

// RecordWithShortcut records lineage and creates a COW shortcut if the
// parent has a shortcut to an ancestor.
func (m *ManagerCOW) RecordWithShortcut(ctx context.Context, root cid.Cid, parent cid.Cid, arcCount int) error {
	// Record the lineage
	if err := m.Record(ctx, root, parent, arcCount); err != nil {
		return err
	}

	// If parent has a shortcut, create a shortcut from root to that ancestor
	if parent.Defined() {
		shortcut, err := m.store.GetShortcut(ctx, parent)
		if err == nil && shortcut != nil {
			dist := shortcut.Dist + 1
			_ = m.store.RecordShortcut(ctx, root, shortcut.To, dist)
		}
	}

	return nil
}

// GetAncestorFast retrieves an ancestor using COW shortcuts for faster lookup.
// Falls back to linear traversal if no shortcut exists.
func (m *ManagerCOW) GetAncestorFast(ctx context.Context, root cid.Cid, maxDepth int) ([]cid.Cid, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root CID is undefined")
	}

	var result []cid.Cid
	current := root
	depth := 0

	for {
		// Try shortcut first
		shortcut, err := m.store.GetShortcut(ctx, current)
		if err == nil && shortcut != nil && shortcut.To.Defined() {
			result = append(result, shortcut.To)
			current = shortcut.To
			depth += shortcut.Dist
			if maxDepth > 0 && depth >= maxDepth {
				break
			}
			continue
		}

		// Fall back to linear traversal
		rec, err := m.Get(ctx, current)
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
