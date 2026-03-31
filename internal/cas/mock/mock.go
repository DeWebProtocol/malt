// Package mock provides a mock CAS implementation for testing.
package mock

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/internal/cas"
	"github.com/dewebprotocol/malt/key"
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
func (m *CAS) Get(ctx context.Context, k key.Key) ([]byte, error) {
	if k.Kind() != key.KeyKindPayloadCID {
		return nil, fmt.Errorf("expected PayloadCID, got %v", k.Kind())
	}

	data, ok := m.blocks[k.String()]
	if !ok {
		return nil, fmt.Errorf("block not found: %s", k.String())
	}

	return data, nil
}

// Put stores a block in mock storage.
func (m *CAS) Put(ctx context.Context, data []byte) (key.Key, error) {
	k, err := key.NewPayloadCID(data)
	if err != nil {
		return nil, err
	}

	m.blocks[k.String()] = data
	return k, nil
}

// Has checks if a block exists in mock storage.
func (m *CAS) Has(ctx context.Context, k key.Key) (bool, error) {
	_, ok := m.blocks[k.String()]
	return ok, nil
}

// AddBlock adds a pre-existing block to mock storage.
func (m *CAS) AddBlock(k key.Key, data []byte) {
	m.blocks[k.String()] = data
}

// Ensure CAS implements cas.Client.
var _ cas.Client = (*CAS)(nil)