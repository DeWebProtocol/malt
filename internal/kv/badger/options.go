// Package badger provides a BadgerDB-based KVStore implementation.
package badger

// Option configures a BadgerDB KVStore.
type Option func(*options)

type options struct {
	path     string
	inMemory bool
}

func defaultOptions() *options {
	return &options{
		path:     "./data/malt.db",
		inMemory: false,
	}
}

// WithPath sets the database path.
func WithPath(path string) Option {
	return func(o *options) {
		o.path = path
	}
}

// WithInMemory sets whether to run in memory mode.
func WithInMemory(inMemory bool) Option {
	return func(o *options) {
		o.inMemory = inMemory
	}
}