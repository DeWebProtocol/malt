// Package arctable provides the Explicit Arc Table (ArcTable) abstraction.
// This file contains common utilities and interfaces shared by ArcTable implementations.
package arctable

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/arctable/bloom"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// BloomFilterManager provides unified bloom filter management across ArcTable implementations.
type BloomFilterManager struct {
	bloomCache *bloom.BloomCache
}

// NewBloomFilterManager creates a new bloom filter manager.
func NewBloomFilterManager(bloomCache *bloom.BloomCache) *BloomFilterManager {
	return &BloomFilterManager{
		bloomCache: bloomCache,
	}
}

// Checker checks if a path might exist in the cache using bloom filter.
func (bfm *BloomFilterManager) MightContain(bucketId, path string) bool {
	if bfm.bloomCache == nil {
		return true // Bloom disabled, assume it might exist
	}
	// Note: This is a simplified synchronous check; actual implementation
	// in BloomCache may be async. Callers should wrap async calls as needed.
	result, err := bfm.bloomCache.MightContain(nil, bucketId, path)
	if err != nil {
		return true // On error, assume it might exist
	}
	return result
}

// CreateBucket creates a new bucket with custom bloom configuration.
func (bfm *BloomFilterManager) CreateBucket(ctx context.Context, bucketId string, cfg *bloom.BucketConfig) error {
	if bfm.bloomCache == nil {
		return fmt.Errorf("bloom cache not configured")
	}
	return bfm.bloomCache.CreateBucket(ctx, bucketId, cfg)
}

// Insert records that a path exists in the bloom filter.
func (bfm *BloomFilterManager) Insert(ctx context.Context, bucketId, path string) error {
	if bfm.bloomCache == nil {
		return nil // Bloom disabled
	}
	return bfm.bloomCache.Add(ctx, bucketId, []string{path})
}

// Delete records that a path no longer exists in the bloom filter.
// Note: BloomCache does not support path deletion, only bucket-level operations.
// This is a no-op to maintain interface consistency.
func (bfm *BloomFilterManager) Delete(ctx context.Context, bucketId, path string) error {
	if bfm.bloomCache == nil {
		return nil // Bloom disabled
	}
	// Bloom filter does not support single path deletion
	// Callers should invalidate the entire bucket if needed
	return nil
}

// Enabled returns whether bloom filter is enabled.
func (bfm *BloomFilterManager) Enabled() bool {
	return bfm.bloomCache != nil
}

// AddBatch adds multiple paths to the bloom filter at once.
func (bfm *BloomFilterManager) AddBatch(ctx context.Context, bucketId string, paths []string) error {
	if bfm.bloomCache == nil {
		return nil
	}
	return bfm.bloomCache.Add(ctx, bucketId, paths)
}

// MightContainBatch checks multiple paths at once using bloom filter.
func (bfm *BloomFilterManager) MightContainBatch(ctx context.Context, bucketId string, paths []string) (map[string]bool, error) {
	if bfm.bloomCache == nil {
		result := make(map[string]bool)
		for _, p := range paths {
			result[p] = true
		}
		return result, nil
	}
	return bfm.bloomCache.MightContainBatch(ctx, bucketId, paths)
}

// GetBloomCache returns the underlying BloomCache for advanced operations.
// This is intended for internal use by ArcTable implementations that need direct access.
func (bfm *BloomFilterManager) GetBloomCache() *bloom.BloomCache {
	return bfm.bloomCache
}

// Helper function for common key generation patterns

// DefaultArcKey generates the standard arc key format.
// Used by overwrite ArcTable implementation.
func DefaultArcKey(bucketId string, path arcset.Path) []byte {
	// Format: bucketId:path
	return []byte(bucketId + ":" + path.String())
}

// DefaultBucketPrefix generates the standard bucket prefix format.
// Used by overwrite ArcTable implementation.
func DefaultBucketPrefix(bucketId string) []byte {
	// Format: bucketId:
	return []byte(bucketId + ":")
}

// VersionedArcKey generates a versioned arc key format.
// Used by versioned ArcTable implementation to include version information.
func VersionedArcKey(bucketId string, version cid.Cid, path arcset.Path) []byte {
	// Format: bucketId:version:path
	return []byte(bucketId + ":" + version.String() + ":" + path.String())
}

// VersionedBucketPrefix generates a versioned bucket prefix format.
// Used by versioned ArcTable implementation to include version information.
func VersionedBucketPrefix(bucketId string, version cid.Cid) []byte {
	// Format: bucketId:version:
	return []byte(bucketId + ":" + version.String() + ":")
}

// RootKeyFormat generates the key for root->bucketId mapping.
// This is shared across all ArcTable implementations.
func RootKeyFormat(root cid.Cid) []byte {
	// Format: root:{cid}
	return []byte("root:" + root.String())
}
