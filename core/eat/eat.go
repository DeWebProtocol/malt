// Package eat defines the Explicit Arc Table interface and implementations.
// EAT is an internal component for fast lookup of arc targets.
// It provides NO correctness guarantee - SCE is responsible for verification.
package eat

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ErrNotFound is returned when an arc is not found.
var ErrNotFound = fmt.Errorf("arc not found")

// IsNotFound checks if an error is ErrNotFound.
func IsNotFound(err error) bool {
	return err == ErrNotFound
}

// EAT (Explicit Arc Table) stores arc entries for fast lookup.
// It maps (root, path) -> target CID.
type EAT interface {
	// Get retrieves the target CID for (root, path).
	// Returns ErrNotFound if not found.
	Get(root cid.Cid, path string) (cid.Cid, error)

	// Put stores an arc entry.
	Put(root cid.Cid, path string, target cid.Cid) error

	// Delete removes an arc entry.
	Delete(root cid.Cid, path string) error

	// View returns an ArcSetView for a specific root.
	// This allows EAT to be used directly as an ArcSetView source.
	View(root cid.Cid) arcset.View

	// Close releases resources.
	Close() error
}