// Package mock provides a mock CAS implementation for testing.
package mock

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/cas"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// CAS is a mock CAS implementation for testing.
type CAS struct {
	blocks map[string][]byte
}

// NewCAS creates a new mock CAS.
func NewCAS() *CAS {
	return &CAS{
		blocks: make(map[string][]byte),
	}
}

// Get retrieves a block from mock storage.
func (m *CAS) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	data, ok := m.blocks[c.String()]
	if !ok {
		return nil, fmt.Errorf("block not found: %s", c.String())
	}

	return data, nil
}

// Put stores a block in mock storage.
func (m *CAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	// Create a CID with raw codec and sha2-256 hash
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	c := cid.NewCidV1(cid.Raw, mhash)

	m.blocks[c.String()] = data
	return c, nil
}

// Has checks if a block exists in mock storage.
func (m *CAS) Has(ctx context.Context, c cid.Cid) (bool, error) {
	_, ok := m.blocks[c.String()]
	return ok, nil
}

// AddBlock adds a pre-existing block to mock storage.
func (m *CAS) AddBlock(c cid.Cid, data []byte) {
	m.blocks[c.String()] = data
}

// Ensure CAS implements cas.Client.
var _ cas.Client = (*CAS)(nil)