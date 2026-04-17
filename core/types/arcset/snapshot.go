// Package arcset defines interfaces for arc set views.
// An arc set is a collection of canonical path -> CID mappings.
package arcset

import (
	"errors"

	cid "github.com/ipfs/go-cid"
)

// ErrNotFound is returned when an arc is not found.
var ErrNotFound = errors.New("arc not found")

// Iterator iterates over arcs in an arc set.
type Iterator interface {
	// Next advances to the next arc.
	// Returns (path, cid, true) if there is an arc, or (_, _, false) if done.
	Next() (path Path, c cid.Cid, ok bool)

	// Err returns any error encountered during iteration.
	Err() error

	// Close releases iterator resources.
	// Must be called when iteration is complete.
	Close()
}

// ArcSet provides an immutable view of an arc set.
// It supports random access and stable iteration over canonical path -> CID bindings.
type ArcSet interface {
	// Get retrieves the target CID for a canonical path.
	Get(path Path) (cid.Cid, bool)

	// Iterate returns an iterator over all arcs.
	Iterate() Iterator

	// Len returns the number of arcs.
	Len() int
}
