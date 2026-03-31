// Package kv provides a key-value store interface and implementations.
package kv

import (
	"context"
	"fmt"

	"github.com/dgraph-io/badger/v4"
)

// BadgerKV is a BadgerDB implementation of KVStore.
type BadgerKV struct {
	db *badger.DB
}

// BadgerConfig holds configuration for BadgerKV.
type BadgerConfig struct {
	// Path is the directory for the database files.
	Path string

	// InMemory runs Badger in memory mode (useful for testing).
	InMemory bool
}

// NewBadgerKV creates a new BadgerDB-backed KV store.
func NewBadgerKV(cfg *BadgerConfig) (*BadgerKV, error) {
	if cfg == nil {
		cfg = &BadgerConfig{InMemory: true}
	}

	opts := badger.DefaultOptions(cfg.Path)
	opts.Logger = nil // Disable Badger logging
	if cfg.InMemory {
		opts.InMemory = true
	}

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open BadgerDB: %w", err)
	}

	return &BadgerKV{db: db}, nil
}

// Get retrieves a value by key.
func (b *BadgerKV) Get(ctx context.Context, key []byte) ([]byte, error) {
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
			return nil, ErrNotFound
		}
		return nil, err
	}
	return result, nil
}

// Put stores a key-value pair.
func (b *BadgerKV) Put(ctx context.Context, key, value []byte) error {
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Set(key, value)
	})
}

// Delete removes a key.
func (b *BadgerKV) Delete(ctx context.Context, key []byte) error {
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete(key)
	})
}

// Has checks if a key exists.
func (b *BadgerKV) Has(ctx context.Context, key []byte) (bool, error) {
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
func (b *BadgerKV) NewIterator(ctx context.Context, start, end []byte) Iterator {
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
func (b *BadgerKV) Batch() Batch {
	return &badgerBatch{db: b.db, wb: b.db.NewWriteBatch()}
}

// Close releases resources.
func (b *BadgerKV) Close() error {
	return b.db.Close()
}

// badgerIterator implements Iterator.
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

// badgerBatch implements Batch.
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

// Ensure BadgerKV implements KVStore.
var _ KVStore = (*BadgerKV)(nil)