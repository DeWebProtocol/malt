// Package content provides compatibility adapters for the optional
// ContentStore interface.
package content

import (
	"context"
	"sync"

	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/kvstore"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// KVStoreContentStore implements the ContentStore compatibility interface
// using KVStore. CID bytes are used as keys, data bytes as values.
type KVStoreContentStore struct {
	kv     kvstore.KVStore
	mu     sync.RWMutex
	closed bool
}

// NewKVStoreContentStore creates a compatibility ContentStore backed by KVStore.
func NewKVStoreContentStore(kv kvstore.KVStore) *KVStoreContentStore {
	return &KVStoreContentStore{
		kv: kv,
	}
}

// cidKey converts a CID to a KVStore key.
func cidKey(c cid.Cid) []byte {
	return c.Bytes()
}

// Get retrieves data by its CID.
func (s *KVStoreContentStore) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}

	data, err := s.kv.Get(ctx, cidKey(c))
	if err != nil {
		if err == kvstore.ErrNotFound {
			return nil, interfaces.ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// Put stores data and returns its CID.
// The CID is derived using SHA-256 multihash.
func (s *KVStoreContentStore) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return cid.Cid{}, interfaces.ErrStoreClosed
	}

	// Generate CID from data using sha2-256
	hash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	c := cid.NewCidV1(cid.Raw, hash)

	// Store data with CID as key
	if err := s.kv.Put(ctx, cidKey(c), data); err != nil {
		return cid.Cid{}, err
	}
	return c, nil
}

// Has checks if data exists for a given CID.
func (s *KVStoreContentStore) Has(ctx context.Context, c cid.Cid) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false, interfaces.ErrStoreClosed
	}
	return s.kv.Has(ctx, cidKey(c))
}

// BatchGet retrieves multiple data blocks by CIDs.
func (s *KVStoreContentStore) BatchGet(ctx context.Context, cids []cid.Cid) (map[string][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}

	keys := make([][]byte, len(cids))
	for i, c := range cids {
		keys[i] = cidKey(c)
	}

	kvResults, err := s.kv.BatchGet(ctx, keys)
	if err != nil {
		return nil, err
	}

	// Convert to CID-keyed results
	results := make(map[string][]byte)
	for _, c := range cids {
		keyStr := string(cidKey(c))
		if data, ok := kvResults[keyStr]; ok {
			results[c.String()] = data
		}
	}
	return results, nil
}

// BatchPut stores multiple data blocks and returns their CIDs.
func (s *KVStoreContentStore) BatchPut(ctx context.Context, datas [][]byte) ([]cid.Cid, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, interfaces.ErrStoreClosed
	}

	batch := s.kv.Batch()
	cids := make([]cid.Cid, len(datas))

	for i, data := range datas {
		// Generate CID
		hash, err := mh.Sum(data, mh.SHA2_256, -1)
		if err != nil {
			batch.Cancel()
			return nil, err
		}
		c := cid.NewCidV1(cid.Raw, hash)
		cids[i] = c

		// Add to batch
		if err := batch.Put(cidKey(c), data); err != nil {
			batch.Cancel()
			return nil, err
		}
	}

	if err := batch.Commit(ctx); err != nil {
		return nil, err
	}
	return cids, nil
}

// Close releases resources.
func (s *KVStoreContentStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.kv.Close()
}

// Ensure KVStoreContentStore implements ContentStore.
var _ interfaces.ContentStore = (*KVStoreContentStore)(nil)
