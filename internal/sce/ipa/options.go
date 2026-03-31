// Package ipa provides Inner Product Argument commitment implementation.
package ipa

// Option configures an IPA commitment scheme.
type Option func(*options)

type options struct {
	vectorSize int
}

func defaultOptions() *options {
	return &options{
		vectorSize: 256,
	}
}

// WithVectorSize sets the maximum vector size.
func WithVectorSize(size int) Option {
	return func(o *options) {
		o.vectorSize = size
	}
}