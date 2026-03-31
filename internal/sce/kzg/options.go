// Package kzg provides KZG polynomial commitment implementation.
package kzg

import (
	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
)

// Option configures a KZG commitment scheme.
type Option func(*options)

type options struct {
	context    *gokzg4844.Context
	vectorSize int
	cacheSize  int
}

// Default options.
func defaultOptions() *options {
	return &options{
		vectorSize: 4096,
		cacheSize:  1000,
	}
}

// WithContext sets a custom KZG context (for custom trusted setups).
func WithContext(ctx *gokzg4844.Context) Option {
	return func(o *options) {
		o.context = ctx
	}
}

// WithVectorSize sets the maximum vector size.
func WithVectorSize(size int) Option {
	return func(o *options) {
		o.vectorSize = size
	}
}

// WithCacheSize sets the proof cache size.
func WithCacheSize(size int) Option {
	return func(o *options) {
		o.cacheSize = size
	}
}