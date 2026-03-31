// Package verkle provides Verkle tree commitment implementation.
package verkle

// Option configures a Verkle commitment scheme.
type Option func(*options)

type options struct {
	cacheSize int
}

func defaultOptions() *options {
	return &options{
		cacheSize: 1000,
	}
}

// WithCacheSize sets the proof cache size.
func WithCacheSize(size int) Option {
	return func(o *options) {
		o.cacheSize = size
	}
}