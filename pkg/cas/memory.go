// Package cas provides a mock in-memory Content-Addressed Storage implementation.
package cas

import (
	"sync"

	"github.com/dewebprotocol/malt/pkg/types"
)

// MemoryCAS is an in-memory implementation of CAS for testing.
// It is safe for concurrent use.
type MemoryCAS struct {
	mu    sync.RWMutex
	data  map[string][]byte // CID string -> data
	stats map[string]Stat   // CID string -> stat
}

// NewMemoryCAS creates a new in-memory CAS.
func NewMemoryCAS() *MemoryCAS {
	return &MemoryCAS{
		data:  make(map[string][]byte),
		stats: make(map[string]Stat),
	}
}

// Put stores data and returns its CID.
func (c *MemoryCAS) Put(data []byte) (types.CID, error) {
	cid, err := types.NewCID(data)
	if err != nil {
		return types.CID{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if already exists (deduplication)
	key := cid.String()
	if _, exists := c.data[key]; !exists {
		c.data[key] = data
		c.stats[key] = Stat{
			Size:        int64(len(data)),
			CID:         cid,
			Replication: 1,
		}
	}

	return cid, nil
}

// Get retrieves data by CID.
func (c *MemoryCAS) Get(cid types.CID) ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := cid.String()
	data, ok := c.data[key]
	if !ok {
		return nil, ErrNotFound
	}

	// Return a copy to prevent modification
	result := make([]byte, len(data))
	copy(result, data)
	return result, nil
}

// Has checks if data exists for the given CID.
func (c *MemoryCAS) Has(cid types.CID) (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	_, ok := c.data[cid.String()]
	return ok, nil
}

// Delete removes data for the given CID.
func (c *MemoryCAS) Delete(cid types.CID) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cid.String()
	if _, ok := c.data[key]; !ok {
		return ErrNotFound
	}

	delete(c.data, key)
	delete(c.stats, key)
	return nil
}

// Stat returns information about stored data.
func (c *MemoryCAS) Stat(cid types.CID) (Stat, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stat, ok := c.stats[cid.String()]
	if !ok {
		return Stat{}, ErrNotFound
	}

	return stat, nil
}

// PutMany stores multiple objects.
func (c *MemoryCAS) PutMany(data [][]byte) ([]types.CID, error) {
	cids := make([]types.CID, len(data))
	for i, d := range data {
		cid, err := c.Put(d)
		if err != nil {
			return nil, err
		}
		cids[i] = cid
	}
	return cids, nil
}

// GetMany retrieves multiple objects.
func (c *MemoryCAS) GetMany(cids []types.CID) ([][]byte, error) {
	result := make([][]byte, len(cids))
	for i, cid := range cids {
		data, err := c.Get(cid)
		if err != nil {
			return nil, err
		}
		result[i] = data
	}
	return result, nil
}

// Clear removes all stored data.
func (c *MemoryCAS) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data = make(map[string][]byte)
	c.stats = make(map[string]Stat)
}

// Size returns the number of stored objects.
func (c *MemoryCAS) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.data)
}

// TotalBytes returns the total size of stored data.
func (c *MemoryCAS) TotalBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var total int64
	for _, data := range c.data {
		total += int64(len(data))
	}
	return total
}

// Ensure MemoryCAS implements interfaces
var _ CAS = (*MemoryCAS)(nil)
var _ BatchCAS = (*MemoryCAS)(nil)