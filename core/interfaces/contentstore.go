// Package interfaces defines the shared interfaces used by the MALT codebase.
package interfaces

import (
	"context"
	"errors"

	cid "github.com/ipfs/go-cid"
)

// Common errors for store interfaces.
var (
	// ErrNotFound is returned when a key/CID is not found.
	ErrNotFound = errors.New("not found")

	// ErrStoreClosed is returned when operating on a closed store.
	ErrStoreClosed = errors.New("store is closed")
)

// ContentStore is an optional compatibility abstraction for storing and
// retrieving data by CID. The canonical MALT path uses CAS clients directly.
type ContentStore interface {
	// Get retrieves data by its CID.
	// Returns error if the CID is not found.
	Get(ctx context.Context, c cid.Cid) ([]byte, error)

	// Put stores data and returns its CID.
	// The CID is derived from the data content.
	Put(ctx context.Context, data []byte) (cid.Cid, error)

	// Has checks if data exists for a given CID.
	Has(ctx context.Context, c cid.Cid) (bool, error)

	// BatchGet retrieves multiple data blocks by CIDs in a single operation.
	// Returns a map of CID string -> data for CIDs that were found.
	// CIDs not found are omitted from the result map.
	BatchGet(ctx context.Context, cids []cid.Cid) (map[string][]byte, error)

	// BatchPut stores multiple data blocks and returns their CIDs.
	// Returns a map of original data index -> CID.
	BatchPut(ctx context.Context, datas [][]byte) ([]cid.Cid, error)

	// Close releases resources.
	Close() error
}
