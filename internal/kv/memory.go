// Package kv provides a key-value store interface and implementations.
package kv

import (
	"context"
	"sync"
)

// MemoryKV is an in-memory implementation of KVStore.
// Useful for testing and development.
type MemoryKV struct {
	mu    sync.RWMutex
	data  map[string][]byte
}

// NewMemoryKV creates a new in-memory KV store.
func NewMemoryKV() *MemoryKV {
	return &MemoryKV{
		data: make(map[string][]byte),
	}
}

// Get retrieves a value by key.
func (m *MemoryKV) Get(ctx context.Context, key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	v, ok := m.data[string(key)]
	if !ok {
		return nil, ErrNotFound
	}

	// Return a copy to prevent mutation
	result := make([]byte, len(v))
	copy(result, v)
	return result, nil
}

// Put stores a key-value pair.
func (m *MemoryKV) Put(ctx context.Context, key, value []byte) error {
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
func (m *MemoryKV) Delete(ctx context.Context, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, string(key))
	return nil
}

// Has checks if a key exists.
func (m *MemoryKV) Has(ctx context.Context, key []byte) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.data[string(key)]
	return ok, nil
}

// NewIterator creates an iterator over keys.
func (m *MemoryKV) NewIterator(ctx context.Context, start, end []byte) Iterator {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Collect all keys in range
	var keys [][]byte
	for k := range m.data {
		keys = append(keys, []byte(k))
	}

	return &memoryIterator{
		kv:    m,
		keys:  keys,
		index: -1,
	}
}

// Batch returns a batch writer.
func (m *MemoryKV) Batch() Batch {
	return &memoryBatch{kv: m, ops: make([]batchOp, 0)}
}

// Close releases resources (no-op for memory).
func (m *MemoryKV) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = nil
	return nil
}

// memoryIterator implements Iterator.
type memoryIterator struct {
	kv    *MemoryKV
	keys  [][]byte
	index int
	err   error
}

func (it *memoryIterator) Next() bool {
	it.index++
	return it.index < len(it.keys)
}

func (it *memoryIterator) Key() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	return it.keys[it.index]
}

func (it *memoryIterator) Value() []byte {
	if it.index < 0 || it.index >= len(it.keys) {
		return nil
	}
	v, _ := it.kv.Get(context.Background(), it.keys[it.index])
	return v
}

func (it *memoryIterator) Err() error {
	return it.err
}

func (it *memoryIterator) Close() {}

// batchOp represents a batch operation.
type batchOp struct {
	op    int // 0=put, 1=delete
	key   []byte
	value []byte
}

// memoryBatch implements Batch.
type memoryBatch struct {
	kv  *MemoryKV
	ops []batchOp
}

func (b *memoryBatch) Put(key, value []byte) error {
	k := make([]byte, len(key))
	v := make([]byte, len(value))
	copy(k, key)
	copy(v, value)
	b.ops = append(b.ops, batchOp{op: 0, key: k, value: v})
	return nil
}

func (b *memoryBatch) Delete(key []byte) error {
	k := make([]byte, len(key))
	copy(k, key)
	b.ops = append(b.ops, batchOp{op: 1, key: k})
	return nil
}

func (b *memoryBatch) Commit(ctx context.Context) error {
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

func (b *memoryBatch) Cancel() {
	b.ops = nil
}

// Ensure MemoryKV implements KVStore.
var _ KVStore = (*MemoryKV)(nil)