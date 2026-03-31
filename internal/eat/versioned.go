// Package eat defines the Explicit Arc Table interface and implementations.
package eat

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/dgraph-io/badger/v4"
	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/key"
)

// VersionedEAT is a versioned EAT implementation using BadgerDB.
// It stores path-based history: path -> [(index, target), ...]
// with metadata: root -> index.
type VersionedEAT struct {
	mu   sync.RWMutex
	db   *badger.DB
}

// VersionedEATConfig holds configuration for VersionedEAT.
type VersionedEATConfig struct {
	// Dir is the database directory. Empty for in-memory.
	Dir string
}

// NewVersionedEAT creates a new VersionedEAT.
func NewVersionedEAT(cfg *VersionedEATConfig) (*VersionedEAT, error) {
	if cfg == nil {
		cfg = &VersionedEATConfig{}
	}

	opts := badger.DefaultOptions(cfg.Dir)
	if cfg.Dir == "" {
		opts = badger.DefaultOptions("").WithInMemory(true)
	}

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger: %w", err)
	}

	return &VersionedEAT{db: db}, nil
}

// Key prefixes for different buckets.
var (
	prefixMeta  = []byte("m:")  // m:root -> index
	prefixArcs  = []byte("a:")  // a:path -> [(index, key), ...]
)

// metaKey generates a key for the metadata bucket.
func metaKey(k key.Key) []byte {
	return append(prefixMeta, k.Bytes()...)
}

// arcsKey generates a key for the arcs bucket.
func arcsKey(path string) []byte {
	return append(prefixArcs, []byte(path)...)
}

// Get retrieves the target key for (root, path).
// It performs versioned lookup using binary search on path history.
func (e *VersionedEAT) Get(root key.Key, path string) (key.Key, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Get the index for this root
	var idx uint64
	err := e.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(metaKey(root))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			if len(v) != 8 {
				return fmt.Errorf("invalid index value length: %d", len(v))
			}
			idx = binary.BigEndian.Uint64(v)
			return nil
		})
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get root index: %w", err)
	}

	// Get the path history
	var history []historyEntry
	err = e.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(arcsKey(path))
		if err != nil {
			return err
		}
		return item.Value(func(v []byte) error {
			var err error
			history, err = decodeHistory(v)
			return err
		})
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get path history: %w", err)
	}

	// Binary search: find largest index <= idx
	entry := binarySearchHistory(history, idx)
	if entry == nil {
		return nil, ErrNotFound
	}

	return entry.key, nil
}

// Put stores an arc entry and updates metadata.
func (e *VersionedEAT) Put(root key.Key, path string, target key.Key) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.db.Update(func(txn *badger.Txn) error {
		// Get or create index for this root
		var idx uint64
		item, err := txn.Get(metaKey(root))
		if err == badger.ErrKeyNotFound {
			// New root, find next index
			idx = 0
			// TODO: scan for max index
		} else if err != nil {
			return fmt.Errorf("failed to get root index: %w", err)
		} else {
			err = item.Value(func(v []byte) error {
				idx = binary.BigEndian.Uint64(v)
				return nil
			})
			if err != nil {
				return err
			}
		}

		// Get existing history
		var history []historyEntry
		item, err = txn.Get(arcsKey(path))
		if err == nil {
			err = item.Value(func(v []byte) error {
				history, err = decodeHistory(v)
				return err
			})
			if err != nil {
				return err
			}
		} else if err != badger.ErrKeyNotFound {
			return fmt.Errorf("failed to get path history: %w", err)
		}

		// Append new entry
		history = append(history, historyEntry{index: idx, key: target})
		sort.Slice(history, func(i, j int) bool {
			return history[i].index < history[j].index
		})

		// Store updated history
		encoded, err := encodeHistory(history)
		if err != nil {
			return fmt.Errorf("failed to encode history: %w", err)
		}
		return txn.Set(arcsKey(path), encoded)
	})
}

// Delete removes an arc entry.
func (e *VersionedEAT) Delete(root key.Key, path string) error {
	// For versioned EAT, we don't actually delete, we set to nil
	// or we could add a tombstone entry
	return fmt.Errorf("delete not supported in versioned EAT, use update with nil")
}

// View returns an ArcSetView for a specific root.
func (e *VersionedEAT) View(root key.Key) sce.ArcSetView {
	return &versionedEATView{eat: e, root: root}
}

// Close releases resources.
func (e *VersionedEAT) Close() error {
	return e.db.Close()
}

// historyEntry represents a single entry in the path history.
type historyEntry struct {
	index uint64
	key   key.Key
}

// encodeHistory encodes a history slice to bytes.
// Format: [count:4][entry1][entry2]...
// Each entry: [index:8][key_len:4][key_bytes][kind:1]
func encodeHistory(history []historyEntry) ([]byte, error) {
	// Calculate total size
	total := 4 // count
	for _, e := range history {
		total += 8 + 4 + len(e.key.Bytes()) + 1
	}

	result := make([]byte, total)
	binary.BigEndian.PutUint32(result[0:4], uint32(len(history)))

	offset := 4
	for _, e := range history {
		binary.BigEndian.PutUint64(result[offset:offset+8], e.index)
		offset += 8

		keyBytes := e.key.Bytes()
		binary.BigEndian.PutUint32(result[offset:offset+4], uint32(len(keyBytes)))
		offset += 4

		copy(result[offset:offset+len(keyBytes)], keyBytes)
		offset += len(keyBytes)

		result[offset] = byte(e.key.Kind())
		offset += 1
	}

	return result, nil
}

// decodeHistory decodes bytes to a history slice.
func decodeHistory(data []byte) ([]historyEntry, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("data too short for history")
	}

	count := binary.BigEndian.Uint32(data[0:4])
	history := make([]historyEntry, 0, count)

	offset := 4
	for i := uint32(0); i < count; i++ {
		if len(data) < offset+8 {
			return nil, fmt.Errorf("unexpected end of data at index %d", i)
		}
		index := binary.BigEndian.Uint64(data[offset : offset+8])
		offset += 8

		if len(data) < offset+4 {
			return nil, fmt.Errorf("unexpected end of data at key length %d", i)
		}
		keyLen := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4

		if len(data) < offset+int(keyLen)+1 {
			return nil, fmt.Errorf("unexpected end of data at key %d", i)
		}
		keyBytes := data[offset : offset+int(keyLen)]
		offset += int(keyLen)

		kind := key.KeyKind(data[offset])
		offset += 1

		var k key.Key
		var err error
		switch kind {
		case key.KeyKindStructureRoot:
			k = key.NewStructureRoot(keyBytes)
		case key.KeyKindPayloadCID:
			k, err = key.NewPayloadCIDFromBytes(keyBytes)
			if err != nil {
				return nil, fmt.Errorf("failed to decode payload CID: %w", err)
			}
		default:
			return nil, fmt.Errorf("unknown key kind: %d", kind)
		}

		history = append(history, historyEntry{index: index, key: k})
	}

	return history, nil
}

// binarySearchHistory finds the entry with the largest index <= target.
func binarySearchHistory(history []historyEntry, target uint64) *historyEntry {
	if len(history) == 0 {
		return nil
	}

	// Find first entry with index > target
	i := sort.Search(len(history), func(i int) bool {
		return history[i].index > target
	})

	if i == 0 {
		return nil
	}

	return &history[i-1]
}

// versionedEATView implements ArcSetView for VersionedEAT.
type versionedEATView struct {
	eat  *VersionedEAT
	root key.Key
	idx  uint64
}

// Get retrieves the target key for a path.
func (v *versionedEATView) Get(path string) (key.Key, bool) {
	k, err := v.eat.Get(v.root, path)
	if err != nil {
		return nil, false
	}
	return k, true
}

// Iterate returns an iterator over all arcs for the root.
// TODO: This requires scanning all paths, which is expensive.
// Consider maintaining a separate index of paths per root.
func (v *versionedEATView) Iterate() sce.ArcIterator {
	// For now, return empty iterator
	// This should be improved for production use
	return &emptyIterator{}
}

// Len returns the number of arcs.
// TODO: This requires scanning, consider caching.
func (v *versionedEATView) Len() int {
	return 0
}

// emptyIterator is a placeholder iterator.
type emptyIterator struct{}

func (it *emptyIterator) Next() (string, key.Key, bool) {
	return "", nil, false
}

func (it *emptyIterator) Err() error {
	return nil
}