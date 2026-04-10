// Package api provides functional options for Node configuration.
package api

import (
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/sce/commitment"
)

// Option configures a MALT Node.
type Option func(*options)

type options struct {
	// Configuration file path
	configFile string

	// Pre-built components (override config-based creation)
	kvStore    kvstore.KVStore
	commitment commitment.Scheme
	eat        eat.EAT
	cas        cas.Client
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
		// Config will be processed by NewNode
	}
}

// WithKVStore sets a custom KVStore implementation.
func WithKVStore(store kvstore.KVStore) Option {
	return func(o *options) {
		o.kvStore = store
	}
}

// WithCommitment sets a custom commitment scheme.
func WithCommitment(scheme commitment.Scheme) Option {
	return func(o *options) {
		o.commitment = scheme
	}
}

// WithEAT sets a custom EAT implementation.
func WithEAT(e eat.EAT) Option {
	return func(o *options) {
		o.eat = e
	}
}

// WithCAS sets a custom CAS client.
func WithCAS(c cas.Client) Option {
	return func(o *options) {
		o.cas = c
	}
}
