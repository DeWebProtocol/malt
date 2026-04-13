// Package content provides ContentStore implementations.
package content

import (
	"context"
	"sync"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/interfaces"
	cid "github.com/ipfs/go-cid"
)

// CASContentStore wraps a CAS client to implement ContentStore.
type CASContentStore struct {
	cas    cas.Client
	mu     sync.RWMutex
	closed bool
}

// NewCASContentStore creates a new ContentStore wrapping a CAS client.
func NewCASContentStore(cas cas.Client) *CASContentStore {
	return &CASContentStore{
		cas: cas,
	}
}

// Get retrieves data by its CID.
func (s *CASContentStore) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}
	return s.cas.Get(ctx, c)
}

// Put stores data and returns its CID.
func (s *CASContentStore) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return cid.Cid{}, interfaces.ErrStoreClosed
	}
	return s.cas.Put(ctx, data)
}

// Has checks if data exists for a given CID.
func (s *CASContentStore) Has(ctx context.Context, c cid.Cid) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, interfaces.ErrStoreClosed
	}
	return s.cas.Has(ctx, c)
}

// BatchGet retrieves multiple data blocks by CIDs.
func (s *CASContentStore) BatchGet(ctx context.Context, cids []cid.Cid) (map[string][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}

	results := make(map[string][]byte)
	for _, c := range cids {
		data, err := s.cas.Get(ctx, c)
		if err != nil {
			// Skip CIDs not found, continue with others
			continue
		}
		results[c.String()] = data
	}
	return results, nil
}

// BatchPut stores multiple data blocks and returns their CIDs.
func (s *CASContentStore) BatchPut(ctx context.Context, datas [][]byte) ([]cid.Cid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}

	cids := make([]cid.Cid, len(datas))
	for i, data := range datas {
		c, err := s.cas.Put(ctx, data)
		if err != nil {
			return nil, err
		}
		cids[i] = c
	}
	return cids, nil
}

// Close releases resources.
func (s *CASContentStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Ensure CASContentStore implements ContentStore.
var _ interfaces.ContentStore = (*CASContentStore)(nil)