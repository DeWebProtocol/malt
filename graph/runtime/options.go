package runtimegraph

import (
	"github.com/dewebprotocol/malt/auth/commitment"
)

// Option configures a Graph instance.
type Option func(*Options)

// Options holds configuration for Graph creation.
type Options struct {
	Scheme    commitment.IndexCommitment
	Namespace string
}

func defaultOptions() *Options {
	return &Options{}
}

// WithCommitmentScheme sets the commitment scheme for this Graph.
// Default: KZG.
func WithCommitmentScheme(scheme commitment.IndexCommitment) Option {
	return func(o *Options) {
		o.Scheme = scheme
	}
}

// WithNamespace sets the ArcSet materializer namespace for this Graph.
// Default: the graph's ID.
func WithNamespace(id string) Option {
	return func(o *Options) {
		o.Namespace = id
	}
}
