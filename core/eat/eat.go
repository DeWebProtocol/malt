// Package eat defines the Explicit Arc Table interface and implementations.
// EAT is an internal component for fast lookup of arc targets.
// It provides NO correctness guarantee - SCE is responsible for verification.
package eat

import (
	"fmt"

	"github.com/dewebprotocol/malt/types/arcset"
	"github.com/dewebprotocol/malt/key"
)

// ErrNotFound is returned when an arc is not found.
var ErrNotFound = fmt.Errorf("arc not found")

// IsNotFound checks if an error is ErrNotFound.
func IsNotFound(err error) bool {
	return err == ErrNotFound
}

// EAT (Explicit Arc Table) stores arc entries for fast lookup.
// It maps (root, path) -> target key.
type EAT interface {
	// Get retrieves the target key for (root, path).
	// Returns ErrNotFound if not found.
	Get(root key.Key, path string) (key.Key, error)

	// Put stores an arc entry.
	Put(root key.Key, path string, target key.Key) error

	// Delete removes an arc entry.
	Delete(root key.Key, path string) error

	// View returns an ArcSetView for a specific root.
	// This allows EAT to be used directly as an ArcSetView source.
	View(root key.Key) arcset.View

	// Close releases resources.
	Close() error
}