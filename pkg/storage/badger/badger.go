// Package badger provides a BadgerDB-based storage implementation for MALT.
package badger

import (
	"fmt"
	"sync"

	"github.com/dgraph-io/badger/v4"
	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/storage"
	"github.com/dewebprotocol/malt/pkg/types"
)

// BadgerStorage implements storage.LineageStore using BadgerDB.
type BadgerStorage struct {
	db     *badger.DB
	path   string
	closed bool
	mu     sync.RWMutex
}

// Config holds BadgerDB configuration.
type Config struct {
	// Path is the directory path for the database
	Path string

	// InMemory runs BadgerDB in memory mode
	InMemory bool

	// SyncWrites enables sync writes
	SyncWrites bool
}

// DefaultConfig returns the default BadgerDB configuration.
func DefaultConfig() *Config {
	return &Config{
		InMemory:   true,
		SyncWrites: false,
	}
}

// New creates a new BadgerDB storage.
func New(cfg *Config) (*BadgerStorage, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	opts := badger.DefaultOptions(cfg.Path)
	opts.InMemory = cfg.InMemory
	opts.SyncWrites = cfg.SyncWrites

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open BadgerDB: %w", err)
	}

	return &BadgerStorage{
		db:   db,
		path: cfg.Path,
	}, nil
}

// NewInMemory creates an in-memory BadgerDB storage.
func NewInMemory() (*BadgerStorage, error) {
	return New(&Config{InMemory: true})
}

// NewAtPath creates a BadgerDB storage at the specified path.
func NewAtPath(path string) (*BadgerStorage, error) {
	return New(&Config{Path: path, InMemory: false})
}

// Get retrieves the target CID for a commitment and path.
func (s *BadgerStorage) Get(comm commitment.Commitment, p types.Path) (types.CID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return types.CID{}, storage.ErrClosed
	}

	key := storage.MakeKey(comm, p)

	var result []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		result, err = item.ValueCopy(nil)
		return err
	})

	if err != nil {
		if err == badger.ErrKeyNotFound {
			return types.CID{}, storage.ErrNotFound
		}
		return types.CID{}, fmt.Errorf("BadgerDB get failed: %w", err)
	}

	// Parse CID from bytes
	cid, err := types.ParseCID(string(result))
	if err != nil {
		return types.CID{}, fmt.Errorf("failed to parse CID: %w", err)
	}

	return cid, nil
}

// Put stores an arc entry.
func (s *BadgerStorage) Put(comm commitment.Commitment, p types.Path, c types.CID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	key := storage.MakeKey(comm, p)

	err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, []byte(c.String()))
	})

	if err != nil {
		return fmt.Errorf("BadgerDB put failed: %w", err)
	}

	return nil
}

// Delete removes an arc entry.
func (s *BadgerStorage) Delete(comm commitment.Commitment, p types.Path) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	key := storage.MakeKey(comm, p)

	err := s.db.Update(func(txn *badger.Txn) error {
		_, err := txn.Get(key)
		if err != nil {
			return err
		}
		return txn.Delete(key)
	})

	if err != nil {
		if err == badger.ErrKeyNotFound {
			return storage.ErrNotFound
		}
		return fmt.Errorf("BadgerDB delete failed: %w", err)
	}

	return nil
}

// Has checks if an entry exists for (comm, p).
func (s *BadgerStorage) Has(comm commitment.Commitment, p types.Path) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return false, storage.ErrClosed
	}

	key := storage.MakeKey(comm, p)

	var exists bool
	err := s.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(key)
		if err == nil {
			exists = true
			return nil
		}
		if err == badger.ErrKeyNotFound {
			exists = false
			return nil
		}
		return err
	})

	return exists, err
}

// Batch executes multiple operations atomically.
func (s *BadgerStorage) Batch(ops []storage.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	err := s.db.Update(func(txn *badger.Txn) error {
		for _, op := range ops {
			key := op.Entry.Key()
			switch op.Type {
			case storage.OpPut:
				if err := txn.Set(key, []byte(op.Entry.Target.String())); err != nil {
				return fmt.Errorf("put failed for key %s: %w", key, err)
				}
			case storage.OpDelete:
				if err := txn.Delete(key); err != nil {
				return fmt.Errorf("delete failed for key %s: %w", key, err)
				}
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("BadgerDB batch failed: %w", err)
	}

	return nil
}

// Iterate iterates over all entries for a commitment.
func (s *BadgerStorage) Iterate(comm commitment.Commitment) (storage.Iter, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	// Create prefix for this commitment
	prefix := make([]byte, 4+len(comm))
	prefix[0] = byte(len(comm) >> 24)
	prefix[1] = byte(len(comm) >> 16)
	prefix[2] = byte(len(comm) >> 8)
	prefix[3] = byte(len(comm))
	copy(prefix[4:], comm)

	var entries []storage.EATEntry

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()

			comm, path, err := storage.ParseKey(key)
			if err != nil {
				continue
			}

			var value []byte
			if err := item.Value(func(v []byte) error {
				value = v
				return nil
			}); err != nil {
				return err
			}

			cid, err := types.ParseCID(string(value))
			if err != nil {
				continue
			}

			entries = append(entries, storage.EATEntry{
				Commitment: comm,
				Path:       path,
				Target:     cid,
			})
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("BadgerDB iterate failed: %w", err)
	}

	return &badgerIter{entries: entries}, nil
}

// Close closes the storage.
func (s *BadgerStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	return s.db.Close()
}

// SetParent records a parent-child relationship.
func (s *BadgerStorage) SetParent(childComm, parentComm commitment.Commitment) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return storage.ErrClosed
	}

	// Key format: "lineage:" + child commitment
	key := []byte("lineage:" + childComm.String())

	err := s.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, parentComm)
	})

	if err != nil {
		return fmt.Errorf("BadgerDB set parent failed: %w", err)
	}

	return nil
}

// GetParent returns the parent commitment of the given commitment.
func (s *BadgerStorage) GetParent(comm commitment.Commitment) (commitment.Commitment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	key := []byte("lineage:" + comm.String())

	var parent []byte
	err := s.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		parent, err = item.ValueCopy(nil)
		return err
	})

	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, storage.ErrNotFound
		}
		return nil, fmt.Errorf("BadgerDB get parent failed: %w", err)
	}

	return commitment.Commitment(parent), nil
}

// GetLineage returns the full lineage from the given commitment to the root.
func (s *BadgerStorage) GetLineage(comm commitment.Commitment) ([]commitment.Commitment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	var lineage []commitment.Commitment
	current := comm

	for {
		lineage = append(lineage, current)

		// Try to get parent
		key := []byte("lineage:" + current.String())
		var parent []byte
		err := s.db.View(func(txn *badger.Txn) error {
			item, err := txn.Get(key)
			if err != nil {
				return err
			}
			parent, err = item.ValueCopy(nil)
			return err
		})

		if err != nil {
			if err == badger.ErrKeyNotFound {
				break // No more parents
			}
			return nil, fmt.Errorf("BadgerDB get lineage failed: %w", err)
		}

		current = commitment.Commitment(parent)
	}

	return lineage, nil
}

// GetLatest returns the latest commitment in a lineage.
func (s *BadgerStorage) GetLatest(rootComm commitment.Commitment) (commitment.Commitment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, storage.ErrClosed
	}

	// Build reverse map: parent -> child
	children := make(map[string]commitment.Commitment)

	err := s.db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte("lineage:")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := item.Key()

			// Extract child from key
			childStr := string(key[len("lineage:"):])

			var parent []byte
			if err := item.Value(func(v []byte) error {
				parent = v
				return nil
			}); err != nil {
				return err
			}

			children[string(parent)] = commitment.Commitment(childStr)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("BadgerDB get latest failed: %w", err)
	}

	// Traverse from root to find the latest
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
func (s *BadgerStorage) Stats() storage.Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return storage.Stats{}
	}

	lsm, vlog := s.db.Size()
	return storage.Stats{
		SizeBytes: lsm + vlog,
	}
}

// badgerIter implements storage.Iter.
type badgerIter struct {
	entries []storage.EATEntry
	index   int
}

func (i *badgerIter) Next() bool {
	i.index++
	return i.index <= len(i.entries)
}

func (i *badgerIter) Entry() storage.EATEntry {
	if i.index < 1 || i.index > len(i.entries) {
		return storage.EATEntry{}
	}
	return i.entries[i.index-1]
}

func (i *badgerIter) Err() error {
	return nil
}

func (i *badgerIter) Close() {
	// Nothing to do
}

// Ensure BadgerStorage implements interfaces
var _ storage.Storage = (*BadgerStorage)(nil)
var _ storage.LineageStore = (*BadgerStorage)(nil)