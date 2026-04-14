package graph

import (
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce/commitment"
)

// Option configures a Graph instance.
type Option func(*Options)

// Options holds configuration for Graph creation.
type Options struct {
	Scheme          commitment.Scheme
	BucketId        string
	EAT             eat.EAT
	LineageRecorder LineageRecorder
	SCECacheSize    int // 0 = use sce.DefaultCacheSize
}

func defaultOptions() *Options {
	return &Options{}
}

// WithCommitmentScheme sets the commitment scheme for this Graph.
// Default: Verkle tree.
func WithCommitmentScheme(scheme commitment.Scheme) Option {
	return func(o *Options) {
		o.Scheme = scheme
	}
}

// WithBucketId sets the EAT bucket namespace for this Graph.
// Default: the graph's ID.
func WithBucketId(id string) Option {
	return func(o *Options) {
		o.BucketId = id
	}
}

// WithEAT overrides the shared EAT for this Graph.
// Default: uses the Node's shared EAT.
func WithEAT(e eat.EAT) Option {
	return func(o *Options) {
		o.EAT = e
	}
}

// WithLineageRecorder sets an optional lineage recorder for this Graph.
func WithLineageRecorder(rec LineageRecorder) Option {
	return func(o *Options) {
		o.LineageRecorder = rec
	}
}

// WithSCECacheSize sets the SCE session cache size for this Graph.
// Default: sce.DefaultCacheSize (1024). Node.NewGraph() auto-sets this from config.
func WithSCECacheSize(n int) Option {
	return func(o *Options) {
		o.SCECacheSize = n
	}
}
