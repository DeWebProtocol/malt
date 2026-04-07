// Package eat defines the Explicit Arc Table interface and implementations.
// EAT is an internal component for fast lookup of arc targets.
// It provides NO correctness guarantee - SCE is responsible for verification.
package eat

import (
	"errors"

	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ErrNotFound is returned when an arc is not found.
var ErrNotFound = errors.New("arc not found")

// IsNotFound checks if an error is ErrNotFound.
func IsNotFound(err error) bool {
	return err == ErrNotFound
}

// EAT (Explicit Arc Table) stores arc entries for fast lookup.
// It maps (bucketId, path) -> target CID.
// bucketId provides namespace isolation for different graphs.
// Both versioned and non-versioned implementations share this interface.
type EAT interface {
	// Get retrieves the target CID for (bucketId, root, path).
	// bucketId is the namespace for the arc set.
	// For overwrite EAT: root is optional (cid.Undef skips validation).
	// For versioned EAT: root is the version to start the chain lookup.
	// Returns ErrNotFound if not found.
	Get(bucketId string, root cid.Cid, path string) (cid.Cid, error)

	// Update stores arc entries with a new commitment root.
	// bucketId is the namespace for the arc set.
	// For overwrite EAT: oldRoot mappings are invalidated, data is overwritten.
	// For versioned EAT: newRoot is linked to parentRoot via @previous.
	// Use cid.Undef for oldRoot/parentRoot for the first version.
	// If a target CID is cid.Undef, the corresponding arc is deleted.
	Update(bucketId string, newRoot, oldRoot cid.Cid, arcs map[string]cid.Cid) error

	// Snapshot returns an immutable snapshot of all arcs for a given root.
	// The snapshot preloads all data into memory, suitable for random access.
	// For overwrite EAT: root is optional (cid.Undef skips validation).
	// For versioned EAT: includes all ancestor arcs via @previous chain.
	Snapshot(bucketId string, root cid.Cid) arcset.Snapshot

	// Iterate returns a streaming iterator over arcs for a given root.
	// For overwrite EAT: root is optional (cid.Undef skips validation).
	// For versioned EAT: root is the version to iterate (walks @previous chain).
	// Caller must call Close() on the iterator when done.
	Iterate(bucketId string, root cid.Cid) arcset.Iterator

	// Close releases resources.
	Close() error
}
