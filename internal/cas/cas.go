// Package cas provides Content Addressable Storage clients.
package cas

import (
	"context"

	"github.com/dewebprotocol/malt/key"
)

// Client provides access to content-addressable storage.
type Client interface {
	// Get retrieves a block by its CID.
	Get(ctx context.Context, cid key.Key) ([]byte, error)

	// Put stores a block and returns its CID.
	Put(ctx context.Context, data []byte) (key.Key, error)

	// Has checks if a block exists.
	Has(ctx context.Context, cid key.Key) (bool, error)
}