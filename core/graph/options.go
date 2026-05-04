package graph

import (
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/writer"
)

// Option configures a Graph instance.
type Option func(*Options)

// Options holds configuration for Graph creation.
type Options struct {
	Scheme          commitment.IndexCommitment
	Namespace       string
	LineageRecorder writer.LineageRecorder
}

func defaultOptions() *Options {
	return &Options{}
}

// WithCommitmentScheme sets the commitment scheme for this Graph.
// Default: Verkle tree.
func WithCommitmentScheme(scheme commitment.IndexCommitment) Option {
	return func(o *Options) {
		o.Scheme = scheme
	}
}

// WithNamespace sets the ArcTable namespace for this Graph.
// Default: the graph's ID.
func WithNamespace(id string) Option {
	return func(o *Options) {
		o.Namespace = id
	}
}

// WithLineageRecorder sets an optional lineage recorder for this Graph.
func WithLineageRecorder(rec writer.LineageRecorder) Option {
	return func(o *Options) {
		o.LineageRecorder = rec
	}
}
