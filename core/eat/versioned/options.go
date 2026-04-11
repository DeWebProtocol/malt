// Package versioned provides a versioned EAT implementation using a KVStore.

package versioned

import (
	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/kvstore"
)

// Option configures an EAT instance.
type Option func(*options)

type options struct {
	kv         kvstore.KVStore
	bloomCache *bloom.BloomCache
}

// defaultOptions returns default EAT options.
func defaultOptions() *options {
	return &options{}
}

// WithKVStore sets the KVStore backend for the EAT.
func WithKVStore(kv kvstore.KVStore) Option {
	return func(o *options) {
		o.kv = kv
	}
}

// WithBloomCache enables the BloomCache for fast negative lookups.
func WithBloomCache(bloomCache *bloom.BloomCache) Option {
	return func(o *options) {
		o.bloomCache = bloomCache
	}
}
