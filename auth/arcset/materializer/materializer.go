// Package materializer defines the minimal untrusted ArcSet loading and
// materialization capabilities consumed by MALT execution algorithms.
//
// The interfaces deliberately say nothing about persistence, indexing,
// transactions, namespaces on disk, or an ArcTable implementation. A gateway
// may satisfy them with memory, a KV-backed ArcTable, SQL, object storage, or a
// remote service. Portable verification does not depend on this package.
package materializer

import (
	"context"
	"errors"

	"github.com/dewebprotocol/malt/auth/arcset"
	cid "github.com/ipfs/go-cid"
)

// ErrNotFound reports that a requested coordinate is absent from the supplied
// ArcSet materialization.
var ErrNotFound = arcset.ErrNotFound

// IsNotFound reports whether err represents an absent materialized arc.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// Store is an injected capability for opening and materializing ArcSet views.
// Scope is opaque execution context chosen by the caller; MALT does not assign
// tenant, graph, bucket, or persistence meaning to it.
type Store interface {
	Get(context.Context, string, cid.Cid, arcset.Path) (cid.Cid, error)
	BatchGet(context.Context, string, cid.Cid, []arcset.Path) (map[arcset.Path]cid.Cid, error)
	// Update materializes a new immutable ArcSet root relative to an optional
	// previous root. Transaction and persistence semantics remain caller-owned.
	Update(context.Context, string, cid.Cid, cid.Cid, arcset.ArcSet) error
	Snapshot(context.Context, string, cid.Cid) (arcset.ArcSet, error)
	Iterate(context.Context, string, cid.Cid) arcset.Iterator
}

// BranchingStore is an optional capability for materializers that preserve
// concurrent children of the same parent root.
type BranchingStore interface {
	SupportsConcurrentBranches() bool
}
