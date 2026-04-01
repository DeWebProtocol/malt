// Package versioned provides a versioned EAT implementation using a KVStore.
package versioned

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/arcset"
	"github.com/dewebprotocol/malt/eat"
	"github.com/dewebprotocol/malt/kv"
	"github.com/dewebprotocol/malt/key"
)

// EAT is a versioned EAT implementation using a KVStore.
// It stores path-based history: path -> [(index, target), ...]
// with metadata: root -> index.
type EAT struct {
	mu sync.RWMutex
	kv kv.KVStore
}

// NewEAT creates a new VersionedEAT with the given KVStore.
func NewEAT(store kv.KVStore) (*EAT, error) {
	if store == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{kv: store}, nil
}

// Key prefixes for different buckets.
var (
	prefixMeta = []byte("m:") // m:root -> index
	prefixArcs = []byte("a:") // a:path -> [(index, key), ...]
	prefixIdx  = []byte("i:") // i:counter -> next index
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
func (e *EAT) Get(root key.Key, path string) (key.Key, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Get the index for this root
	idxBytes, err := e.kv.Get(nil, metaKey(root))
	if err != nil {
		if err == kv.ErrNotFound {
			return nil, eat.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get root index: %w", err)
	}

	if len(idxBytes) != 8 {
		return nil, fmt.Errorf("invalid index value length: %d", len(idxBytes))
	}
	idx := binary.BigEndian.Uint64(idxBytes)

	// Get the path history
	historyBytes, err := e.kv.Get(nil, arcsKey(path))
	if err != nil {
		if err == kv.ErrNotFound {
			return nil, eat.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get path history: %w", err)
	}

	history, err := decodeHistory(historyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to decode history: %w", err)
	}

	// Binary search: find largest index <= idx
	entry := binarySearchHistory(history, idx)
	if entry == nil {
		return nil, eat.ErrNotFound
	}

	return entry.key, nil
}

// Put stores an arc entry and updates metadata.
func (e *EAT) Put(root key.Key, path string, target key.Key) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Get or create index for this root
	idxBytes, err := e.kv.Get(nil, metaKey(root))
	var idx uint64
	if err == kv.ErrNotFound {
		// New root, get next index
		idx = e.getNextIndex()
	} else if err != nil {
		return fmt.Errorf("failed to get root index: %w", err)
	} else {
		idx = binary.BigEndian.Uint64(idxBytes)
	}

	// Store root -> index mapping
	newIdxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(newIdxBytes, idx)
	if err := e.kv.Put(nil, metaKey(root), newIdxBytes); err != nil {
		return fmt.Errorf("failed to store root index: %w", err)
	}

	// Get existing history
	var history []historyEntry
	historyBytes, err := e.kv.Get(nil, arcsKey(path))
	if err == nil {
		history, err = decodeHistory(historyBytes)
		if err != nil {
			return fmt.Errorf("failed to decode history: %w", err)
		}
	} else if err != kv.ErrNotFound {
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
	return e.kv.Put(nil, arcsKey(path), encoded)
}

// getNextIndex returns the next available index.
func (e *EAT) getNextIndex() uint64 {
	idxBytes, err := e.kv.Get(nil, prefixIdx)
	if err == kv.ErrNotFound {
		// First index
		idx := uint64(0)
		idxBytes = make([]byte, 8)
		binary.BigEndian.PutUint64(idxBytes, idx+1)
		e.kv.Put(nil, prefixIdx, idxBytes)
		return idx
	} else if err != nil {
		return 0
	}

	idx := binary.BigEndian.Uint64(idxBytes)
	newIdx := make([]byte, 8)
	binary.BigEndian.PutUint64(newIdx, idx+1)
	e.kv.Put(nil, prefixIdx, newIdx)
	return idx
}

// Delete removes an arc entry.
func (e *EAT) Delete(root key.Key, path string) error {
	return fmt.Errorf("delete not supported in versioned EAT, use update with nil")
}

// View returns an ArcSetView for a specific root.
func (e *EAT) View(root key.Key) arcset.View {
	return &eatView{eat: e, root: root}
}

// Close releases resources.
func (e *EAT) Close() error {
	return e.kv.Close()
}

// historyEntry represents a single entry in the path history.
type historyEntry struct {
	index uint64
	key   key.Key
}

// encodeHistory encodes a history slice to bytes.
func encodeHistory(history []historyEntry) ([]byte, error) {
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

	i := sort.Search(len(history), func(i int) bool {
		return history[i].index > target
	})

	if i == 0 {
		return nil
	}

	return &history[i-1]
}

// eatView implements ArcSetView for VersionedEAT.
type eatView struct {
	eat  *EAT
	root key.Key
	idx  uint64
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
func (v *eatView) Iterate() arcset.Iterator {
	return &emptyIterator{}
}

// Len returns the number of arcs.
func (v *eatView) Len() int {
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

// Ensure EAT implements eat.EAT.
var _ eat.EAT = (*EAT)(nil)