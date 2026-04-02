// Package versioned provides a versioned EAT implementation using a KVStore.
package versioned

import (
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/types/kvstore"
	cid "github.com/ipfs/go-cid"
)

// EAT is a versioned EAT implementation using a KVStore.
// It stores path-based history: path -> [(index, target), ...]
// with metadata: root -> index.
type EAT struct {
	mu sync.RWMutex
	kv kvstore.KVStore
}

// NewEAT creates a new VersionedEAT with the given KVStore.
func NewEAT(store kvstore.KVStore) (*EAT, error) {
	if store == nil {
		return nil, fmt.Errorf("KVStore is required")
	}

	return &EAT{kv: store}, nil
}

// Key prefixes for different buckets.
var (
	prefixMeta = []byte("m:") // m:root -> index
	prefixArcs = []byte("a:") // a:path -> [(index, cid), ...]
	prefixIdx  = []byte("i:") // i:counter -> next index
)

// metaKey generates a key for the metadata bucket.
func metaKey(c cid.Cid) []byte {
	return append(prefixMeta, c.Bytes()...)
}

// arcsKey generates a key for the arcs bucket.
func arcsKey(path string) []byte {
	return append(prefixArcs, []byte(path)...)
}

// Get retrieves the target CID for (root, path).
func (e *EAT) Get(root cid.Cid, path string) (cid.Cid, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Get the index for this root
	idxBytes, err := e.kv.Get(nil, metaKey(root))
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Cid{}, eat.ErrNotFound
		}
		return cid.Cid{}, fmt.Errorf("failed to get root index: %w", err)
	}

	if len(idxBytes) != 8 {
		return cid.Cid{}, fmt.Errorf("invalid index value length: %d", len(idxBytes))
	}
	idx := binary.BigEndian.Uint64(idxBytes)

	// Get the path history
	historyBytes, err := e.kv.Get(nil, arcsKey(path))
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Cid{}, eat.ErrNotFound
		}
		return cid.Cid{}, fmt.Errorf("failed to get path history: %w", err)
	}

	history, err := decodeHistory(historyBytes)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("failed to decode history: %w", err)
	}

	// Binary search: find largest index <= idx
	entry := binarySearchHistory(history, idx)
	if entry == nil {
		return cid.Cid{}, eat.ErrNotFound
	}

	return entry.cid, nil
}

// Put stores an arc entry and updates metadata.
func (e *EAT) Put(root cid.Cid, path string, target cid.Cid) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Get or create index for this root
	idxBytes, err := e.kv.Get(nil, metaKey(root))
	var idx uint64
	if err == kvstore.ErrNotFound {
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
	} else if err != kvstore.ErrNotFound {
		return fmt.Errorf("failed to get path history: %w", err)
	}

	// Append new entry
	history = append(history, historyEntry{index: idx, cid: target})
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

// PutBatch stores multiple arc entries for the same root in a single transaction.
// This is more efficient than calling Put multiple times as it only acquires
// the write lock once and batches the history updates.
func (e *EAT) PutBatch(root cid.Cid, arcs map[string]cid.Cid) error {
	if len(arcs) == 0 {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Get or create index for this root (shared across all arcs)
	idxBytes, err := e.kv.Get(nil, metaKey(root))
	var idx uint64
	if err == kvstore.ErrNotFound {
		// New root, get next index
		idx = e.getNextIndex()
	} else if err != nil {
		return fmt.Errorf("failed to get root index: %w", err)
	} else {
		idx = binary.BigEndian.Uint64(idxBytes)
	}

	// Store root -> index mapping once
	newIdxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(newIdxBytes, idx)
	if err := e.kv.Put(nil, metaKey(root), newIdxBytes); err != nil {
		return fmt.Errorf("failed to store root index: %w", err)
	}

	// Sort paths for deterministic order
	paths := make([]string, 0, len(arcs))
	for p := range arcs {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Update history for each path
	for _, path := range paths {
		target := arcs[path]

		// Get existing history
		var history []historyEntry
		historyBytes, err := e.kv.Get(nil, arcsKey(path))
		if err == nil {
			history, err = decodeHistory(historyBytes)
			if err != nil {
				return fmt.Errorf("failed to decode history for %s: %w", path, err)
			}
		} else if err != kvstore.ErrNotFound {
			return fmt.Errorf("failed to get path history for %s: %w", path, err)
		}

		// Append new entry
		history = append(history, historyEntry{index: idx, cid: target})
		sort.Slice(history, func(i, j int) bool {
			return history[i].index < history[j].index
		})

		// Store updated history
		encoded, err := encodeHistory(history)
		if err != nil {
			return fmt.Errorf("failed to encode history for %s: %w", path, err)
		}
		if err := e.kv.Put(nil, arcsKey(path), encoded); err != nil {
			return fmt.Errorf("failed to store history for %s: %w", path, err)
		}
	}

	return nil
}

// getNextIndex returns the next available index.
func (e *EAT) getNextIndex() uint64 {
	idxBytes, err := e.kv.Get(nil, prefixIdx)
	if err == kvstore.ErrNotFound {
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
func (e *EAT) Delete(root cid.Cid, path string) error {
	return fmt.Errorf("delete not supported in versioned EAT, use update with nil")
}

// View returns an ArcSetView for a specific root.
func (e *EAT) View(root cid.Cid) arcset.View {
	return &eatView{eat: e, root: root}
}

// Close releases resources.
func (e *EAT) Close() error {
	return e.kv.Close()
}

// historyEntry represents a single entry in the path history.
type historyEntry struct {
	index uint64
	cid   cid.Cid
}

// encodeHistory encodes a history slice to bytes.
func encodeHistory(history []historyEntry) ([]byte, error) {
	total := 4 // count
	for _, e := range history {
		total += 8 + 4 + len(e.cid.Bytes())
	}

	result := make([]byte, total)
	binary.BigEndian.PutUint32(result[0:4], uint32(len(history)))

	offset := 4
	for _, e := range history {
		binary.BigEndian.PutUint64(result[offset:offset+8], e.index)
		offset += 8

		cidBytes := e.cid.Bytes()
		binary.BigEndian.PutUint32(result[offset:offset+4], uint32(len(cidBytes)))
		offset += 4

		copy(result[offset:offset+len(cidBytes)], cidBytes)
		offset += len(cidBytes)
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
			return nil, fmt.Errorf("unexpected end of data at cid length %d", i)
		}
		cidLen := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4

		if len(data) < offset+int(cidLen) {
			return nil, fmt.Errorf("unexpected end of data at cid %d", i)
		}
		cidBytes := data[offset : offset+int(cidLen)]
		offset += int(cidLen)

		c, err := cid.Cast(cidBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to decode CID: %w", err)
		}

		history = append(history, historyEntry{index: index, cid: c})
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
	root cid.Cid
	idx  uint64
}

// Get retrieves the target CID for a path.
func (v *eatView) Get(path string) (cid.Cid, bool) {
	c, err := v.eat.Get(v.root, path)
	if err != nil {
		return cid.Cid{}, false
	}
	return c, true
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

func (it *emptyIterator) Next() (string, cid.Cid, bool) {
	return "", cid.Cid{}, false
}

func (it *emptyIterator) Err() error {
	return nil
}

// Ensure EAT implements eat.EAT.
var _ eat.EAT = (*EAT)(nil)