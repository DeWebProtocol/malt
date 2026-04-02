package memory

import (
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/eat"
	cid "github.com/ipfs/go-cid"
)

// BucketedInMemoryEAT manages multiple InMemoryArcSet instances by bucket (graph) ID.
// It implements eat.EAT for use as an in-memory EAT backend.
type BucketedInMemoryEAT struct {
	mu      sync.RWMutex
	buckets map[string]*InMemoryArcSet // bucket key -> arc set
}

// NewBucketedInMemoryEAT creates a new BucketedInMemoryEAT.
func NewBucketedInMemoryEAT() *BucketedInMemoryEAT {
	return &BucketedInMemoryEAT{
		buckets: make(map[string]*InMemoryArcSet),
	}
}

// bucketKey generates a storage key for a root.
func bucketKey(c cid.Cid) string {
	return c.String()
}

// Get retrieves the target CID for (root, path).
func (e *BucketedInMemoryEAT) Get(root cid.Cid, path string) (cid.Cid, error) {
	e.mu.RLock()
	bucket, ok := e.buckets[bucketKey(root)]
	e.mu.RUnlock()

	if !ok {
		return cid.Cid{}, eat.ErrNotFound
	}

	target, ok := bucket.Get(path)
	if !ok {
		return cid.Cid{}, eat.ErrNotFound
	}
	return target, nil
}

// Put stores an arc entry, creating the bucket if needed.
func (e *BucketedInMemoryEAT) Put(root cid.Cid, path string, target cid.Cid) error {
	e.mu.Lock()
	key := bucketKey(root)
	bucket, ok := e.buckets[key]
	if !ok {
		bucket = NewInMemoryArcSet()
		e.buckets[key] = bucket
	}
	e.mu.Unlock()

	bucket.Set(path, target)
	return nil
}

// PutBatch stores multiple arc entries for the same root in a single transaction.
// This is more efficient than calling Put multiple times.
func (e *BucketedInMemoryEAT) PutBatch(root cid.Cid, arcs map[string]cid.Cid) error {
	if len(arcs) == 0 {
		return nil
	}

	e.mu.Lock()
	key := bucketKey(root)
	bucket, ok := e.buckets[key]
	if !ok {
		bucket = NewInMemoryArcSet()
		e.buckets[key] = bucket
	}
	e.mu.Unlock()

	// Set all arcs without reacquiring the bucket lock each time
	for path, target := range arcs {
		bucket.Set(path, target)
	}
	return nil
}

// Delete removes an arc entry.
func (e *BucketedInMemoryEAT) Delete(root cid.Cid, path string) error {
	e.mu.RLock()
	bucket, ok := e.buckets[bucketKey(root)]
	e.mu.RUnlock()

	if !ok {
		return eat.ErrNotFound
	}

	bucket.Delete(path)
	return nil
}

// View returns an ArcSetView for a specific root (bucket).
func (e *BucketedInMemoryEAT) View(root cid.Cid) arcset.View {
	e.mu.RLock()
	bucket, ok := e.buckets[bucketKey(root)]
	e.mu.RUnlock()

	if !ok {
		// Return empty view for non-existent bucket
		return NewInMemoryArcSet()
	}
	return bucket
}

// Close releases resources.
func (e *BucketedInMemoryEAT) Close() error {
	e.mu.Lock()
	e.buckets = nil
	e.mu.Unlock()
	return nil
}

// Ensure BucketedInMemoryEAT implements eat.EAT.
var _ eat.EAT = (*BucketedInMemoryEAT)(nil)