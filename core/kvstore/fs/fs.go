// Package fs provides a filesystem-based implementation of kvstore.KVStore.
// Keys are sharded into subdirectories using the first 2 hex characters,
// similar to Git's object storage layout.
package fs

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/kvstore"
)

const (
	// shardLen is the number of hex characters used for subdirectory names.
	shardLen = 2
)

// KV is a filesystem-based implementation of kvstore.KVStore.
// Keys are stored as files in a sharded directory structure:
//
//	root/
//	├── ab/           # key hex prefix "ab"
//	│   └── cdef...   # remaining hex as filename
//	├── cd/
//	│   └── ef12...
type KV struct {
	root string
	mu   sync.RWMutex
}

// New creates a new filesystem KV store at the given root directory.
// The directory is created if it doesn't exist.
func New(root string) (*KV, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	return &KV{root: root}, nil
}

// keyToPath converts a key to a filesystem path.
// The key is encoded as hex, then sharded: root/<prefix>/<rest>
func (k *KV) keyToPath(key []byte) string {
	hexKey := hex.EncodeToString(key)
	if len(hexKey) < shardLen {
		// Pad with zeros if key is too short
		hexKey = hexKey + "000000000000"
	}
	prefix := hexKey[:shardLen]
	rest := hexKey[shardLen:]
	return filepath.Join(k.root, prefix, rest)
}

// Get retrieves a value by key.
func (k *KV) Get(ctx context.Context, key []byte) ([]byte, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	path := k.keyToPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, kvstore.ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// BatchGet retrieves multiple values by keys.
func (k *KV) BatchGet(ctx context.Context, keys [][]byte) (map[string][]byte, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	results := make(map[string][]byte)
	for _, key := range keys {
		path := k.keyToPath(key)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // Skip not found keys
			}
			return nil, err
		}
		results[string(key)] = data
	}
	return results, nil
}

// Put stores a key-value pair.
func (k *KV) Put(ctx context.Context, key, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	path := k.keyToPath(key)
	dir := filepath.Dir(path)

	// Create subdirectory if needed
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Write to temp file then rename for atomicity
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, value, 0644); err != nil {
		return err
	}

	return os.Rename(tmpPath, path)
}

// Delete removes a key.
func (k *KV) Delete(ctx context.Context, key []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	path := k.keyToPath(key)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Has checks if a key exists.
func (k *KV) Has(ctx context.Context, key []byte) (bool, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	path := k.keyToPath(key)
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// NewIterator creates an iterator over a range of keys.
// Note: filesystem iterator is less efficient than in-memory or database-based iterators.
func (k *KV) NewIterator(ctx context.Context, start, end []byte) kvstore.Iterator {
	k.mu.RLock()
	defer k.mu.RUnlock()

	// Collect all keys from filesystem
	var keys [][]byte

	// Walk through all subdirectories
	entries, err := os.ReadDir(k.root)
	if err != nil {
		return &fsIterator{err: err}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		prefix := entry.Name()

		// Read files in subdirectory
		subdir := filepath.Join(k.root, prefix)
		files, err := os.ReadDir(subdir)
		if err != nil {
			continue
		}

		for _, file := range files {
			if file.IsDir() {
				continue
			}
			// Reconstruct key from prefix + filename
			hexKey := prefix + file.Name()
			key, err := hex.DecodeString(hexKey)
			if err != nil {
				continue
			}

			// Filter by range
			if start != nil && compareKeys(key, start) < 0 {
				continue
			}
			if end != nil && compareKeys(key, end) >= 0 {
				continue
			}

			keys = append(keys, key)
		}
	}

	// Sort keys
	sort.Slice(keys, func(i, j int) bool {
		return compareKeys(keys[i], keys[j]) < 0
	})

	return &fsIterator{
		kv:    k,
		keys:  keys,
		index: -1,
	}
}

// Batch returns a batch writer.
func (k *KV) Batch() kvstore.Batch {
	return &fsBatch{kv: k, ops: make([]batchOp, 0)}
}

// Close releases resources.
func (k *KV) Close() error {
	// No resources to release for filesystem
	return nil
}

// compareKeys compares two keys byte-by-byte.
func compareKeys(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// fsIterator implements kvstore.Iterator.
type fsIterator struct {
	kv    *KV
	keys  [][]byte
	index int
	err   error
}

func (it *fsIterator) Next() bool {
	it.index++
	return it.index < len(it.keys)
}

func (it *fsIterator) Key() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	return it.keys[it.index]
}

func (it *fsIterator) Value() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	v, _ := it.kv.Get(context.Background(), it.keys[it.index])
	return v
}

func (it *fsIterator) Err() error {
	return it.err
}

func (it *fsIterator) Close() {}

// batchOp represents a batch operation.
type batchOp struct {
	op    int // 0=put, 1=delete
	key   []byte
	value []byte
}

// fsBatch implements kvstore.Batch.
type fsBatch struct {
	kv  *KV
	ops []batchOp
}

func (b *fsBatch) Put(key, value []byte) error {
	k := make([]byte, len(key))
	v := make([]byte, len(value))
	copy(k, key)
	copy(v, value)
	b.ops = append(b.ops, batchOp{op: 0, key: k, value: v})
	return nil
}

func (b *fsBatch) Delete(key []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	b.ops = append(b.ops, batchOp{op: 1, key: k})
	return nil
}

func (b *fsBatch) Commit(ctx context.Context) error {
	b.kv.mu.Lock()
	defer b.kv.mu.Unlock()

	for _, op := range b.ops {
		switch op.op {
		case 0:
			// Put
			path := b.kv.keyToPath(op.key)
			dir := filepath.Dir(path)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			tmpPath := path + ".tmp"
			if err := os.WriteFile(tmpPath, op.value, 0644); err != nil {
				return err
			}
			if err := os.Rename(tmpPath, path); err != nil {
				return err
			}
		case 1:
			// Delete
			path := b.kv.keyToPath(op.key)
			os.Remove(path) // ignore error for delete
		}
	}
	return nil
}

func (b *fsBatch) Cancel() {
	b.ops = nil
}

// Ensure KV implements kvstore.KVStore.
var _ kvstore.KVStore = (*KV)(nil)