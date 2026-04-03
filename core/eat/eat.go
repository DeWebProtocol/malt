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
// It maps (root, path) -> target CID.
// Both versioned and non-versioned implementations share this interface.
type EAT interface {
	// Get retrieves the target CID for (root, path).
	// For versioned EAT, walks the @previous chain if needed.
	// Returns ErrNotFound if not found.
	Get(root cid.Cid, path string) (cid.Cid, error)

	// Update stores arc entries with a new commitment root.
	// For non-versioned EAT: oldRoot mappings are deleted, data is overwritten.
	// For versioned EAT: newRoot is linked to parentRoot via @previous.
	// Use cid.Undef for oldRoot/parentRoot for the first version.
	Update(newRoot, oldRoot cid.Cid, arcs map[string]cid.Cid) error

	// View returns an ArcSetView for a specific root.
	// For versioned EAT, the view includes all ancestor arcs.
	View(root cid.Cid) arcset.View

	// Close releases resources.
	Close() error
}
