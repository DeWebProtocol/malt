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
// from out-of-range queries. For a dense stable-indexed list, index < Length
// implies Key is defined; index >= Length implies Key must be cid.Undef.
type Query struct {
	Key    cid.Cid
	Length uint64
}

// RangeMetadata is authenticated measurement metadata for a byte-addressable
// list. ChunkSize is the fixed segment width used to map byte ranges to list
// indexes.
type RangeMetadata struct {
	ChildCount uint64
	TotalSize  uint64
	ChunkSize  uint64
}

// RangeResult is the verifiable result of a byte-range query over a measured
// list. Segments contains the minimal list targets needed to satisfy the
// requested range.
type RangeResult struct {
	Metadata RangeMetadata
	Segments []cid.Cid
}

// Semantics defines the public stable-indexed list semantics.
//
// Commit is the bootstrap path from a materialized list view. All other
// runtime operations execute directly against the committed list root using the
// supplied graph scope rather than caller-managed materialized views.
type Semantics interface {
	// Commit commits the supplied list view into the provided graph scope and
	// returns a structure root.
	Commit(ctx context.Context, namespace string, view View) (cid.Cid, error)

	// Prove proves the key (or absence) at index under root.
	Prove(ctx context.Context, namespace string, root cid.Cid, index uint64) (Query, structure.Proof, error)

	// Verify verifies the proof for an index query under root.
	Verify(root cid.Cid, index uint64, expected Query, proof structure.Proof) (bool, error)

	// Replace performs an index-stable replacement at an existing position.
	Replace(ctx context.Context, namespace string, root cid.Cid, index uint64, oldKey, newKey cid.Cid) (cid.Cid, error)

	// Append extends the list by one key and returns the new root and index.
	Append(ctx context.Context, namespace string, root cid.Cid, key cid.Cid) (newRoot cid.Cid, newIndex uint64, err error)

	// Truncate shrinks the committed length while preserving the remaining prefix.
	Truncate(ctx context.Context, namespace string, root cid.Cid, newLen uint64) (cid.Cid, error)
}

// MeasuredSemantics is an optional list extension for byte-addressable list
// layouts. The range proof is composed from authenticated metadata and the
// index proofs for the minimum segment set covering [start, end). A nil end
// means the authenticated total size.
type MeasuredSemantics interface {
	Semantics

	ProveRange(ctx context.Context, namespace string, root cid.Cid, start uint64, end *uint64) (RangeResult, structure.Proof, error)
	VerifyRange(root cid.Cid, start uint64, end *uint64, expected RangeResult, proof structure.Proof) (bool, error)
}
