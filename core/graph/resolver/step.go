// Package resolver provides resolution logic for Graph.
package resolver

import (
	"context"

	cid "github.com/ipfs/go-cid"
)

// ExplicitStep resolves MALT explicit arcs.
// This uses ArcStore to look up path → CID mappings.
type ExplicitStep interface {
	Resolve(ctx context.Context, root cid.Cid, path string) (matchedPath string, target cid.Cid, evidence interface{}, err error)
}

// ImplicitStep resolves Merkle DAG implicit links.
// This uses ContentStore to fetch and traverse data blocks.
type ImplicitStep interface {
	Resolve(ctx context.Context, root cid.Cid, path string) (matchedPath string, target cid.Cid, evidence interface{}, err error)
}
