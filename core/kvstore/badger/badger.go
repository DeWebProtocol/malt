// Package badger provides a BadgerDB implementation of kvstore.KVStore.
package badger

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/logger"
	"github.com/dgraph-io/badger/v4"
)

// KV is a BadgerDB implementation of kvstore.KVStore.
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
			return nil, kvstore.ErrNotFound
		}
		return nil, err
	}
	return result, nil
}

// BatchGet retrieves multiple values by keys.
// Uses a single transaction with sorted keys for efficient sequential IO.
func (b *KV) BatchGet(ctx context.Context, keys [][]byte) (map[string][]byte, error) {
	start := time.Now()

	if len(keys) == 0 {
		return map[string][]byte{}, nil
	}

	logger.Debug("KV.BatchGet started",
		logger.String("backend", "badger"),
		logger.Int("key_count", len(keys)))

	// Sort keys for sequential traversal (better IO performance on LSM-tree)
	sortedKeys := make([][]byte, len(keys))
	copy(sortedKeys, keys)
	sort.Slice(sortedKeys, func(i, j int) bool {
		return string(sortedKeys[i]) < string(sortedKeys[j])
	})

	results := make(map[string][]byte)
	foundCount := 0

	err := b.db.View(func(txn *badger.Txn) error {
		for _, key := range sortedKeys {
			item, err := txn.Get(key)
			if err != nil {
				if err == badger.ErrKeyNotFound {
					continue // Skip not found keys
				}
				logger.Error("KV.BatchGet transaction error",
					logger.String("backend", "badger"),
					logger.Err(err))
				return err
			}
			val, err := item.ValueCopy(nil)
			if err != nil {
				return err
			}
			results[string(key)] = val
			foundCount++
		}
		return nil
	})
	if err != nil {
		logger.Error("KV.BatchGet failed",
			logger.String("backend", "badger"),
			logger.Err(err))
		return nil, err
	}

	logger.Debug("KV.BatchGet completed",
		logger.String("backend", "badger"),
		logger.Int("key_count", len(keys)),
		logger.Int("found_count", foundCount),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return results, nil
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
func (b *KV) NewIterator(ctx context.Context, start, end []byte) kvstore.Iterator {
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
func (b *KV) Batch() kvstore.Batch {
	return &badgerBatch{db: b.db, wb: b.db.NewWriteBatch()}
}

// Close releases resources.
func (b *KV) Close() error {
	return b.db.Close()
}

// badgerIterator implements kvstore.Iterator.
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

// badgerBatch implements kvstore.Batch.
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

// Ensure KV implements kvstore.KVStore.
var _ kvstore.KVStore = (*KV)(nil)
