// Package api provides functional options for Node configuration.
package api

import (
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/kvstore"
)

// Option configures a MALT Node.
type Option func(*options)

type options struct {
	// Configuration file path
	configFile string
	config     *config.Config

	// Pre-built components (override config-based creation)
	kvStore kvstore.KVStore
	eat     eat.EAT
	cas     cas.Reader
}

func defaultOptions() *options {
	return &options{}
}

// WithConfigFile loads configuration from a file.
func WithConfigFile(path string) Option {
	return func(o *options) {
		o.configFile = path
	}
}

// WithConfig uses the provided config struct.
func WithConfig(cfg *config.Config) Option {
	return func(o *options) {
		o.config = cfg
	}
}

// WithKVStore sets a custom KVStore implementation.
func WithKVStore(store kvstore.KVStore) Option {
	return func(o *options) {
		o.kvStore = store
	}
}

// WithEAT sets a custom EAT implementation.
func WithEAT(e eat.EAT) Option {
	return func(o *options) {
		o.eat = e
	}
}

// WithCAS sets a custom read-side CAS client.
func WithCAS(c cas.Reader) Option {
	return func(o *options) {
		o.cas = c
	}
}
