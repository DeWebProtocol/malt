// Package mapping defines the public keyed-map semantic for MALT.
package mapping

import (
	"context"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/semantic"
	cid "github.com/ipfs/go-cid"
)

// Iterator iterates over a map view in canonical key order.
type Iterator interface {
	Next() (key arcset.Path, value cid.Cid, ok bool)
	Err() error
}

// View exposes a caller-supplied keyed snapshot or materialized view.
type View interface {
	Len() int
	Get(key arcset.Path) (cid.Cid, bool)
	Iterate() Iterator
}

// Binding is the verifiable result for one keyed binding.
//
// Current map semantics emit membership proofs only. Callers should obtain
// absence through current structure state (for example ArcTable lookup or a
// supplied materialized view) rather than expecting a dedicated semantic
// non-membership proof.
type Binding struct {
	Value   cid.Cid
	Present bool
}

// Semantics defines the public keyed-map semantics.
type Semantics interface {
	// Commit commits the supplied map view and returns a structure root.
	Commit(ctx context.Context, namespace string, view View) (cid.Cid, error)

	// Prove proves the existing binding for key under root.
	// It returns an error if key is absent from the committed runtime state.
	Prove(ctx context.Context, namespace string, root cid.Cid, key arcset.Path) (Binding, structure.Proof, error)

	// Verify verifies the proof for a keyed binding under root.
	Verify(root cid.Cid, key arcset.Path, expected Binding, proof structure.Proof) (bool, error)

	// Update applies insert, replace, or delete semantics over the committed
	// runtime state. oldValue=cid.Undef means insert; newValue=cid.Undef means
	// delete.
	Update(ctx context.Context, namespace string, root cid.Cid, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error)
}
