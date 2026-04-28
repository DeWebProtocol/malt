// Package cas provides Content Addressable Storage clients.
package cas

import (
	"context"

	cid "github.com/ipfs/go-cid"
)

// Reader provides read-side access to content-addressable storage.
type Reader interface {
	// Get retrieves a block by its CID.
	Get(ctx context.Context, c cid.Cid) ([]byte, error)

	// Has checks if a block exists.
	Has(ctx context.Context, c cid.Cid) (bool, error)
}

// Writer provides write-side access to content-addressable storage.
type Writer interface {
	// Put stores a block and returns its CID.
	Put(ctx context.Context, data []byte) (cid.Cid, error)
}

// TypedWriter stores blocks under an explicit CID codec.
type TypedWriter interface {
	PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error)
}

// Client provides full read/write access to content-addressable storage.
type Client interface {
	Reader
	Writer
}
