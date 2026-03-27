// Package memory provides an in-memory implementation of the storage interface.
// This is useful for testing and prototyping.
package memory

import (
	"sync"

	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/storage"
	"github.com/dewebprotocol/malt/pkg/types"
)

// MemoryStorage implements Storage using an in-memory map.
// It is safe for concurrent use.
type MemoryStorage struct {
	mu     sync.RWMutex
	entries map[string]storage.EATEntry // key -> entry
	parents map[string]commitment.Commitment // child -> parent
	closed  bool
}

// New creates a new in-memory storage.
func New() *MemoryStorage {
	return &MemoryStorage{
		entries: make(map[string]storage.EATEntry),
		parents: make(map[string]commitment.Commitment),
	}
}

// Get retrieves an entry by commitment and path.
func (s *MemoryStorage) Get(comm commitment.Commitment, p types.Path) (types.CID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return types.CID{}, storage.ErrClosed
	}

	key := string(storage.MakeKey(comm, p))
	entry, ok := s.entries[key]
	if !ok {
		return types.CID{}, storage.ErrNotFound
	}

	return entry.Target, nil
}

// Put stores an entry.
func (s *MemoryStorage) Put(comm commitment.Commitment, p types.Path, c types.CID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	key := string(storage.MakeKey(comm, p))
	s.entries[key] = storage.EATEntry{
		Commitment: comm,
		Path:       p,
		Target:     c,
	}

	return nil
}

// Delete removes an entry.
func (s *MemoryStorage) Delete(comm commitment.Commitment, p types.Path) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	key := string(storage.MakeKey(comm, p))
	if _, ok := s.entries[key]; !ok {
		return storage.ErrNotFound
	}

	delete(s.entries, key)
	return nil
}

// Has checks if an entry exists.
func (s *MemoryStorage) Has(comm commitment.Commitment, p types.Path) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return false, storage.ErrClosed
	}

	key := string(storage.MakeKey(comm, p))
	_, ok := s.entries[key]
	return ok, nil
}

// Batch executes multiple operations atomically.
func (s *MemoryStorage) Batch(ops []storage.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	for _, op := range ops {
		key := string(op.Entry.Key())
		switch op.Type {
		case storage.OpPut:
			s.entries[key] = op.Entry
		case storage.OpDelete:
			delete(s.entries, key)
		}
	}

	return nil
}

// Iterate returns an iterator for all entries under a commitment.
func (s *MemoryStorage) Iterate(comm commitment.Commitment) (storage.Iter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	// Collect all entries for this commitment
	var entries []storage.EATEntry
	for _, entry := range s.entries {
		if entry.Commitment.Equals(comm) {
			entries = append(entries, entry)
		}
	}

	return &memoryIter{entries: entries}, nil
}

// Close closes the storage.
func (s *MemoryStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	s.entries = nil
	s.parents = nil
	return nil
}

// SetParent records a parent-child relationship.
func (s *MemoryStorage) SetParent(childComm, parentComm commitment.Commitment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	s.parents[childComm.String()] = parentComm
	return nil
}

// GetParent returns the parent of a commitment.
func (s *MemoryStorage) GetParent(comm commitment.Commitment) (commitment.Commitment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	parent, ok := s.parents[comm.String()]
	if !ok {
		return nil, storage.ErrNotFound
	}

	return parent, nil
}

// GetLineage returns the full lineage.
func (s *MemoryStorage) GetLineage(comm commitment.Commitment) ([]commitment.Commitment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	var lineage []commitment.Commitment
	current := comm

	for {
		lineage = append(lineage, current)
		parent, ok := s.parents[current.String()]
		if !ok {
			break
		}
		current = parent
	}

	return lineage, nil
}

// GetLatest returns the latest commitment in a lineage.
func (s *MemoryStorage) GetLatest(rootComm commitment.Commitment) (commitment.Commitment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	// Build reverse map: parent -> child
	children := make(map[string]commitment.Commitment)
	for child, parent := range s.parents {
		children[parent.String()] = s.parents[child]
	}

	// Find the latest
	current := rootComm
	for {
		child, ok := children[current.String()]
		if !ok {
			break
		}
		current = child
	}

	return current, nil
}

// Stats returns storage statistics.
func (s *MemoryStorage) Stats() storage.Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	commitments := make(map[string]bool)
	for _, entry := range s.entries {
		commitments[entry.Commitment.String()] = true
	}

	return storage.Stats{
		TotalEntries:     int64(len(s.entries)),
		TotalCommitments: int64(len(commitments)),
	}
}

// Clear removes all entries.
func (s *MemoryStorage) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = make(map[string]storage.EATEntry)
	s.parents = make(map[string]commitment.Commitment)
}

// memoryIter implements storage.Iter.
type memoryIter struct {
	entries []storage.EATEntry
	index   int
}

func (i *memoryIter) Next() bool {
	i.index++
	return i.index <= len(i.entries)
}

func (i *memoryIter) Entry() storage.EATEntry {
	if i.index < 1 || i.index > len(i.entries) {
		return storage.EATEntry{}
	}
	return i.entries[i.index-1]
}

func (i *memoryIter) Err() error {
	return nil
}

func (i *memoryIter) Close() {
	// Nothing to do
}

// Ensure MemoryStorage implements interfaces
var _ storage.Storage = (*MemoryStorage)(nil)
var _ storage.LineageStore = (*MemoryStorage)(nil)