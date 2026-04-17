// Package list defines the public stable-indexed list semantic for MALT.
package list

import (
	"context"

	"github.com/dewebprotocol/malt/core/structure"
	cid "github.com/ipfs/go-cid"
)

// Iterator iterates over a list view in index order.
type Iterator interface {
	Next() (index uint64, value cid.Cid, ok bool)
	Err() error
}

// View exposes a caller-supplied list snapshot or materialized view.
type View interface {
	Len() uint64
	Get(index uint64) (cid.Cid, bool)
	Iterate() Iterator
}

// Query is the verifiable result for a single list position.
// Length is committed state so that verifiers can distinguish in-range slots
// from out-of-range queries.
type Query struct {
	Value   cid.Cid
	Length  uint64
	Present bool
}

// Semantic defines the public stable-indexed list contract.
//
// Native operations are index proof, index-stable replacement, append, and
// length reduction. Insert/delete can be built above this interface by shifting
// values and then applying Append/Truncate as needed.
type Semantic interface {
	// Commit commits the supplied list view and returns a structure root.
	Commit(ctx context.Context, view View) (cid.Cid, error)

	// Prove proves the value (or absence) at index under root.
	Prove(ctx context.Context, root cid.Cid, view View, index uint64) (Query, structure.Proof, error)

	// Verify verifies the proof for an index query under root.
	Verify(root cid.Cid, index uint64, expected Query, proof structure.Proof) (bool, error)

	// Replace performs an index-stable replacement at an existing position.
	Replace(ctx context.Context, root cid.Cid, view View, index uint64, oldValue, newValue cid.Cid) (cid.Cid, error)

	// Append extends the list by one element and returns the new root and index.
	Append(ctx context.Context, root cid.Cid, view View, value cid.Cid) (newRoot cid.Cid, newIndex uint64, err error)

	// Truncate shortens the committed length without changing the prefix that remains.
	Truncate(ctx context.Context, root cid.Cid, view View, newLen uint64) (cid.Cid, error)
}
