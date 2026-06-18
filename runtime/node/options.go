// Package node provides functional options for Node configuration.
package node

import (
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/storage/cas"
	"github.com/dewebprotocol/malt/storage/kv"
)

// Option configures a MALT Node.
type Option func(*options)

type options struct {
	// Configuration file path
	configFile string
	config     *config.Config

	// Pre-built components (override config-based creation)
	kvStore  kvstore.KVStore
	arctable arctable.ArcTable
	cas      cas.Reader

	// disableCASVerification disables the default CID-verifying wrapper around
	// the read-side CAS client. The wrapper is on by default for the
	// config-driven CAS path because the MALT trust model treats CAS as
	// untrusted execution state; only call sites that have already verified
	// content integrity (mocks, in-memory test harnesses) should turn it off.
	disableCASVerification bool

	// forceCASVerification forces the verifying wrapper even when an explicit
	// CAS reader is supplied via WithCAS. Tests that want to exercise the
	// verification path with a mock reader can opt in this way.
	forceCASVerification bool
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

// WithArcTable sets a custom ArcTable implementation.
func WithArcTable(e arctable.ArcTable) Option {
	return func(o *options) {
		o.arctable = e
	}
}

// WithCAS sets a custom read-side CAS client.
func WithCAS(c cas.Reader) Option {
	return func(o *options) {
		o.cas = c
	}
}

// WithoutCASVerification disables the default CID-verifying CAS wrapper.
// Use only when the supplied CAS reader is already trusted (for example an
// in-memory test mock) or when verification is being performed elsewhere in
// the pipeline. Production deployments must keep verification on so the
// daemon does not propagate tampered bytes to clients.
func WithoutCASVerification() Option {
	return func(o *options) {
		o.disableCASVerification = true
	}
}

// WithCASVerification forces the CID-verifying wrapper even when an explicit
// CAS reader is supplied via WithCAS. Useful for integration tests that want
// to exercise the verification path against a mock CAS.
func WithCASVerification() Option {
	return func(o *options) {
		o.forceCASVerification = true
	}
}
