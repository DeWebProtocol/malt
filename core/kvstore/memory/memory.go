// Package memory provides an in-memory implementation of kvstore.KVStore.
package memory

import (
	"bytes"
	"context"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/kvstore"
)

// hasPrefix checks if a byte slice has the given prefix.
func hasPrefix(s, prefix []byte) bool {
	return bytes.HasPrefix(s, prefix)
}

// KV is an in-memory implementation of kvstore.KVStore.
// Useful for testing and development.
type KV struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// New creates a new in-memory KV store.
func New() *KV {
	return &KV{
		data: make(map[string][]byte),
	}
}

// Get retrieves a value by key.
func (m *KV) Get(ctx context.Context, key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	v, ok := m.data[string(key)]
	if !ok {
		return nil, kvstore.ErrNotFound
	}

	// Return a copy to prevent mutation
	result := make([]byte, len(v))
	copy(result, v)
	return result, nil
}

// BatchGet retrieves multiple values by keys.
func (m *KV) BatchGet(ctx context.Context, keys [][]byte) (map[string][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	results := make(map[string][]byte)
	for _, key := range keys {
		v, ok := m.data[string(key)]
		if ok {
			// Return a copy to prevent mutation
			result := make([]byte, len(v))
			copy(result, v)
			results[string(key)] = result
		}
	}
	return results, nil
}

// Put stores a key-value pair.
func (m *KV) Put(ctx context.Context, key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store copies to prevent mutation
	k := make([]byte, len(key))
	v := make([]byte, len(value))
	copy(k, key)
	copy(v, value)

	m.data[string(k)] = v
	return nil
}

// Delete removes a key.
func (m *KV) Delete(ctx context.Context, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, string(key))
	return nil
}

// Has checks if a key exists.
func (m *KV) Has(ctx context.Context, key []byte) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.data[string(key)]
	return ok, nil
}

// NewIterator creates an iterator over keys.
func (m *KV) NewIterator(ctx context.Context, start, end []byte) kvstore.Iterator {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Collect all keys in range
	var keys [][]byte
	for k := range m.data {
		key := []byte(k)
		// Filter by start (prefix) if specified
		if start != nil && !hasPrefix(key, start) {
			continue
		}
		// Filter by end (exclusive upper bound) if specified
		if end != nil && string(key) >= string(end) {
			continue
		}
		keys = append(keys, key)
	}

	// Sort keys for deterministic iteration
	sort.Slice(keys, func(i, j int) bool {
		return string(keys[i]) < string(keys[j])
	})

	return &iterator{
		kv:    m,
		keys:  keys,
		index: -1,
	}
}

// Batch returns a batch writer.
func (m *KV) Batch() kvstore.Batch {
	return &batch{kv: m, ops: make([]batchOp, 0)}
}

// Close releases resources (no-op for memory).
func (m *KV) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = nil
	return nil
}

// iterator implements kvstore.Iterator.
type iterator struct {
	kv    *KV
	keys  [][]byte
	index int
	err   error
}

func (it *iterator) Next() bool {
	it.index++
	return it.index < len(it.keys)
}

func (it *iterator) Key() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	return it.keys[it.index]
}

func (it *iterator) Value() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	v, err := it.kv.Get(context.Background(), it.keys[it.index])
	if err != nil {
		it.err = err
		return nil
	}
	return v
}

func (it *iterator) Err() error {
	return it.err
}

func (it *iterator) Close() {}

// batchOp represents a batch operation.
type batchOp struct {
	op    int // 0=put, 1=delete
	key   []byte
	value []byte
}

// batch implements kvstore.Batch.
type batch struct {
	kv  *KV
	ops []batchOp
}

func (b *batch) Put(key, value []byte) error {
	k := make([]byte, len(key))
	v := make([]byte, len(value))
	copy(k, key)
	copy(v, value)
	b.ops = append(b.ops, batchOp{op: 0, key: k, value: v})
	return nil
}

func (b *batch) Delete(key []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	b.ops = append(b.ops, batchOp{op: 1, key: k})
	return nil
}

func (b *batch) Commit(ctx context.Context) error {
	b.kv.mu.Lock()
	defer b.kv.mu.Unlock()

	for _, op := range b.ops {
		switch op.op {
		case 0:
			b.kv.data[string(op.key)] = op.value
		case 1:
			delete(b.kv.data, string(op.key))
		}
	}
	return nil
}

func (b *batch) Cancel() {
	b.ops = nil
}

// Ensure KV implements kvstore.KVStore.
var _ kvstore.KVStore = (*KV)(nil)
