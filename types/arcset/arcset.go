// Package arcset defines interfaces for arc set views.
// An arc set is a collection of path -> key mappings.
package arcset

import "github.com/dewebprotocol/malt/key"

// Iterator iterates over arcs in an arc set.
type Iterator interface {
	// Next advances to the next arc.
	// Returns (path, key, true) if there is an arc, or (_, _, false) if done.
	Next() (path string, k key.Key, ok bool)

	// Err returns any error encountered during iteration.
	Err() error
}

// View provides a read-only view of an arc set.
type View interface {
	// Get retrieves the target key for a path.
	Get(path string) (key.Key, bool)

	// Iterate returns an iterator over all arcs.
	Iterate() Iterator

	// Len returns the number of arcs.
	Len() int
}