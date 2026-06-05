// Package prefix provides a KVStore view that transparently prefixes keys.
package prefix

import (
	"bytes"
	"context"

	kvstore "github.com/dewebprotocol/malt/storage/kv"
)

// KV is a non-owning prefixed view over another KVStore.
type KV struct {
	inner  kvstore.KVStore
	prefix []byte
}

// New returns a KVStore view that maps every key to prefix + key.
func New(inner kvstore.KVStore, prefix []byte) *KV {
	p := append([]byte(nil), prefix...)
	return &KV{inner: inner, prefix: p}
}

func (k *KV) prefixed(key []byte) []byte {
	out := make([]byte, 0, len(k.prefix)+len(key))
	out = append(out, k.prefix...)
	out = append(out, key...)
	return out
}

func (k *KV) strip(key []byte) []byte {
	if !bytes.HasPrefix(key, k.prefix) {
		return nil
	}
	return append([]byte(nil), key[len(k.prefix):]...)
}

func (k *KV) Get(ctx context.Context, key []byte) ([]byte, error) {
	return k.inner.Get(ctx, k.prefixed(key))
}

func (k *KV) BatchGet(ctx context.Context, keys [][]byte) (map[string][]byte, error) {
	prefixedKeys := make([][]byte, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = k.prefixed(key)
	}
	found, err := k.inner.BatchGet(ctx, prefixedKeys)
	if err != nil {
		return nil, err
	}
	results := make(map[string][]byte, len(found))
	for key, value := range found {
		keyBytes := []byte(key)
		if !bytes.HasPrefix(keyBytes, k.prefix) {
			continue
		}
		results[string(k.strip(keyBytes))] = value
	}
	return results, nil
}

func (k *KV) Put(ctx context.Context, key, value []byte) error {
	return k.inner.Put(ctx, k.prefixed(key), value)
}

func (k *KV) Delete(ctx context.Context, key []byte) error {
	return k.inner.Delete(ctx, k.prefixed(key))
}

func (k *KV) Has(ctx context.Context, key []byte) (bool, error) {
	return k.inner.Has(ctx, k.prefixed(key))
}

func (k *KV) NewIterator(ctx context.Context, start, end []byte) kvstore.Iterator {
	var prefixedStart []byte
	if start == nil {
		prefixedStart = append([]byte(nil), k.prefix...)
	} else {
		prefixedStart = k.prefixed(start)
	}
	var prefixedEnd []byte
	if end != nil {
		prefixedEnd = k.prefixed(end)
	}
	return &iterator{
		inner:  k.inner.NewIterator(ctx, prefixedStart, prefixedEnd),
		prefix: append([]byte(nil), k.prefix...),
	}
}

func (k *KV) Batch() kvstore.Batch {
	return &batch{inner: k.inner.Batch(), prefix: append([]byte(nil), k.prefix...)}
}

// Close is intentionally non-owning; callers must close the wrapped store.
func (k *KV) Close() error {
	return nil
}

type iterator struct {
	inner  kvstore.Iterator
	prefix []byte
	key    []byte
}

func (it *iterator) Next() bool {
	for it.inner.Next() {
		key := it.inner.Key()
		if !bytes.HasPrefix(key, it.prefix) {
			return false
		}
		it.key = append(it.key[:0], key[len(it.prefix):]...)
		return true
	}
	return false
}

func (it *iterator) Key() []byte {
	return append([]byte(nil), it.key...)
}

func (it *iterator) Value() []byte {
	return it.inner.Value()
}

func (it *iterator) Err() error {
	return it.inner.Err()
}

func (it *iterator) Close() {
	it.inner.Close()
}

type batch struct {
	inner  kvstore.Batch
	prefix []byte
}

func (b *batch) prefixed(key []byte) []byte {
	out := make([]byte, 0, len(b.prefix)+len(key))
	out = append(out, b.prefix...)
	out = append(out, key...)
	return out
}

func (b *batch) Put(key, value []byte) error {
	return b.inner.Put(b.prefixed(key), value)
}

func (b *batch) Delete(key []byte) error {
	return b.inner.Delete(b.prefixed(key))
}

func (b *batch) Commit(ctx context.Context) error {
	return b.inner.Commit(ctx)
}

func (b *batch) Cancel() {
	b.inner.Cancel()
}

var _ kvstore.KVStore = (*KV)(nil)
