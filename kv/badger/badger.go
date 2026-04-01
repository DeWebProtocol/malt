// Package badger provides a BadgerDB implementation of kv.KVStore.
package badger

import (
	"context"
	"fmt"

	"github.com/dgraph-io/badger/v4"
	"github.com/dewebprotocol/malt/kv"
)

// KV is a BadgerDB implementation of kv.KVStore.
type KV struct {
	opts *options
	db   *badger.DB
}

// New creates a new BadgerDB-backed KV store with the given options.
func New(opts ...Option) (*KV, error) {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	badgerOpts := badger.DefaultOptions(options.path)
	badgerOpts.Logger = nil // Disable Badger logging
	if options.inMemory {
		badgerOpts.InMemory = true
		badgerOpts.Dir = ""      // Required for in-memory mode
		badgerOpts.ValueDir = "" // Required for in-memory mode
	}

	db, err := badger.Open(badgerOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to open BadgerDB: %w", err)
	}

	return &KV{opts: options, db: db}, nil
}

// Get retrieves a value by key.
func (b *KV) Get(ctx context.Context, key []byte) ([]byte, error) {
	var result []byte
	err := b.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		result, err = item.ValueCopy(nil)
		return err
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return nil, kv.ErrNotFound
		}
		return nil, err
	}
	return result, nil
}

// Put stores a key-value pair.
func (b *KV) Put(ctx context.Context, key, value []byte) error {
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, value)
	})
}

// Delete removes a key.
func (b *KV) Delete(ctx context.Context, key []byte) error {
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(key)
	})
}

// Has checks if a key exists.
func (b *KV) Has(ctx context.Context, key []byte) (bool, error) {
	err := b.db.View(func(txn *badger.Txn) error {
		_, err := txn.Get(key)
		return err
	})
	if err != nil {
		if err == badger.ErrKeyNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// NewIterator creates an iterator over keys.
func (b *KV) NewIterator(ctx context.Context, start, end []byte) kv.Iterator {
	txn := b.db.NewTransaction(false)
	opts := badger.DefaultIteratorOptions
	it := txn.NewIterator(opts)

	return &badgerIterator{
		txn:   txn,
		it:    it,
		start: start,
		end:   end,
		init:  false,
	}
}

// Batch returns a batch writer.
func (b *KV) Batch() kv.Batch {
	return &badgerBatch{db: b.db, wb: b.db.NewWriteBatch()}
}

// Close releases resources.
func (b *KV) Close() error {
	return b.db.Close()
}

// badgerIterator implements kv.Iterator.
type badgerIterator struct {
	txn   *badger.Txn
	it    *badger.Iterator
	start []byte
	end   []byte
	init  bool
	err   error
}

func (it *badgerIterator) Next() bool {
	if !it.init {
		it.it.Rewind()
		if it.start != nil {
			it.it.Seek(it.start)
		}
		it.init = true
	} else {
		it.it.Next()
	}

	if !it.it.Valid() {
		return false
	}

	// Check end bound
	if it.end != nil {
		key := it.it.Item().Key()
		if string(key) >= string(it.end) {
			return false
		}
	}

	return true
}

func (it *badgerIterator) Key() []byte {
	return it.it.Item().Key()
}

func (it *badgerIterator) Value() []byte {
	val, err := it.it.Item().ValueCopy(nil)
	if err != nil {
		it.err = err
		return nil
	}
	return val
}

func (it *badgerIterator) Err() error {
	return it.err
}

func (it *badgerIterator) Close() {
	it.it.Close()
	it.txn.Discard()
}

// badgerBatch implements kv.Batch.
type badgerBatch struct {
	db *badger.DB
	wb *badger.WriteBatch
}

func (b *badgerBatch) Put(key, value []byte) error {
	return b.wb.Set(key, value)
}

func (b *badgerBatch) Delete(key []byte) error {
	return b.wb.Delete(key)
}

func (b *badgerBatch) Commit(ctx context.Context) error {
	return b.wb.Flush()
}

func (b *badgerBatch) Cancel() {
	b.wb.Cancel()
}

// Ensure KV implements kv.KVStore.
var _ kv.KVStore = (*KV)(nil)