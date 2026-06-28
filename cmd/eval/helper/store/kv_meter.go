package store

import (
	"context"

	"github.com/dewebprotocol/malt/storage/kv"
)

// MeteredKV wraps a KVStore and charges every successful put as a changed record.
// The byte counter measures persisted record values, not backend addressing
// keys, to match CAS object-byte accounting.
type MeteredKV struct {
	base     kvstore.KVStore
	meter    *Meter
	category Category
}

// NewMeteredKV wraps base with write accounting.
func NewMeteredKV(base kvstore.KVStore, meter *Meter, category Category) *MeteredKV {
	return &MeteredKV{base: base, meter: meter, category: category}
}

func (m *MeteredKV) Get(ctx context.Context, key []byte) ([]byte, error) {
	return m.base.Get(ctx, key)
}

func (m *MeteredKV) BatchGet(ctx context.Context, keys [][]byte) (map[string][]byte, error) {
	return m.base.BatchGet(ctx, keys)
}

func (m *MeteredKV) Put(ctx context.Context, key, value []byte) error {
	if err := m.base.Put(ctx, key, value); err != nil {
		return err
	}
	m.meter.RecordChangedRecord(m.category, 0, len(value))
	return nil
}

func (m *MeteredKV) Delete(ctx context.Context, key []byte) error {
	if err := m.base.Delete(ctx, key); err != nil {
		return err
	}
	m.meter.RecordChangedRecord(m.category, 0, 0)
	return nil
}

func (m *MeteredKV) Has(ctx context.Context, key []byte) (bool, error) {
	return m.base.Has(ctx, key)
}

func (m *MeteredKV) NewIterator(ctx context.Context, start, end []byte) kvstore.Iterator {
	return m.base.NewIterator(ctx, start, end)
}

func (m *MeteredKV) Batch() kvstore.Batch {
	return &meteredBatch{
		base:     m.base.Batch(),
		meter:    m.meter,
		category: m.category,
	}
}

func (m *MeteredKV) Close() error {
	return m.base.Close()
}

type meteredBatch struct {
	base     kvstore.Batch
	meter    *Meter
	category Category
	puts     []meteredBatchPut
}

type meteredBatchPut struct {
	valueBytes int
}

func (b *meteredBatch) Put(key, value []byte) error {
	if err := b.base.Put(key, value); err != nil {
		return err
	}
	b.puts = append(b.puts, meteredBatchPut{valueBytes: len(value)})
	return nil
}

func (b *meteredBatch) Delete(key []byte) error {
	if err := b.base.Delete(key); err != nil {
		return err
	}
	b.puts = append(b.puts, meteredBatchPut{})
	return nil
}

func (b *meteredBatch) Commit(ctx context.Context) error {
	if err := b.base.Commit(ctx); err != nil {
		return err
	}
	for _, put := range b.puts {
		b.meter.RecordChangedRecord(b.category, 0, put.valueBytes)
	}
	return nil
}

func (b *meteredBatch) Cancel() {
	b.base.Cancel()
}

var _ kvstore.KVStore = (*MeteredKV)(nil)
