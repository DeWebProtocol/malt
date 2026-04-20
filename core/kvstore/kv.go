// Package kvstore defines a key-value store interface for MALT.
// This allows dependency injection - the concrete implementation
// (BadgerDB, in-memory, filesystem, etc.) can be chosen at runtime.
package kvstore

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a key is not found.
var ErrNotFound = errors.New("key not found")

// KVStore is a generic key-value store interface.
// All operations are thread-safe.
type KVStore interface {
	// Get retrieves a value by key.
	// Returns ErrNotFound if the key doesn't exist.
	Get(ctx context.Context, key []byte) ([]byte, error)

	// BatchGet retrieves multiple values by keys in a single operation.
	// Returns a map of key (as string) -> value for keys that were found.
	// Keys not found are omitted from the result map (no error returned).
	// Implementations should optimize for batch access (e.g., sorted traversal,
	// parallel lookup, or native multi-get APIs).
	BatchGet(ctx context.Context, keys [][]byte) (map[string][]byte, error)

	// Put stores a key-value pair.
	Put(ctx context.Context, key, value []byte) error

	// Delete removes a key.
	Delete(ctx context.Context, key []byte) error

	// Has checks if a key exists.
	Has(ctx context.Context, key []byte) (bool, error)

	// NewIterator creates an iterator over a range of keys.
	// start and end are optional; if nil, iterates over all keys.
	NewIterator(ctx context.Context, start, end []byte) Iterator

	// Batch returns a batch writer for atomic operations.
	Batch() Batch

	// Close releases resources.
	Close() error
}

// Iterator iterates over key-value pairs.
type Iterator interface {
	// Next advances to the next entry.
	Next() bool

	// Key returns the current key.
	Key() []byte

	// Value returns the current value.
	Value() []byte

	// Err returns any error encountered.
	Err() error

	// Close releases iterator resources.
	Close()
}

// Batch supports atomic batch writes.
type Batch interface {
	// Put adds a put operation to the batch.
	Put(key, value []byte) error

	// Delete adds a delete operation to the batch.
	Delete(key []byte) error

	// Commit executes all operations atomically.
	Commit(ctx context.Context) error

	// Cancel discards the batch.
	Cancel()
}
