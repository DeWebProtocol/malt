// Package cas provides Content Addressable Storage clients.
package cas

import (
	"context"

	cid "github.com/ipfs/go-cid"
)

// Client provides access to content-addressable storage.
type Client interface {
	// Get retrieves a block by its CID.
	Get(ctx context.Context, c cid.Cid) ([]byte, error)

	// Put stores a block and returns its CID.
	Put(ctx context.Context, data []byte) (cid.Cid, error)

	// Has checks if a block exists.
	Has(ctx context.Context, c cid.Cid) (bool, error)
}
