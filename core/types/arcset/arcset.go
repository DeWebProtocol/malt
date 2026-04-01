// Package arcset defines interfaces for arc set views.
// An arc set is a collection of path -> CID mappings.
package arcset

import cid "github.com/ipfs/go-cid"

// Iterator iterates over arcs in an arc set.
type Iterator interface {
	// Next advances to the next arc.
	// Returns (path, cid, true) if there is an arc, or (_, _, false) if done.
	Next() (path string, c cid.Cid, ok bool)

	// Err returns any error encountered during iteration.
	Err() error
}

// View provides a read-only view of an arc set.
type View interface {
	// Get retrieves the target CID for a path.
	Get(path string) (cid.Cid, bool)

	// Iterate returns an iterator over all arcs.
	Iterate() Iterator

	// Len returns the number of arcs.
	Len() int
}