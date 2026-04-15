// Package eat provides the Explicit Arc Table (EAT) abstraction.
// This file contains common utilities and interfaces shared by EAT implementations.
package eat

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/kvstore"
	cid "github.com/ipfs/go-cid"
)

// KeyBuilder defines the interface for generating EAT storage keys.
// Different EAT implementations (overwrite vs versioned) may have different key formats.
type KeyBuilder interface {
	// ArcKey generates the storage key for a specific arc (path) within a bucket.
	ArcKey(bucketId, path string, version ...cid.Cid) []byte

	// BucketPrefix generates the prefix for all arcs in a bucket.
	BucketPrefix(bucketId string, version ...cid.Cid) []byte

	// RootKey generates the key for root->bucketId mapping.
	RootKey(root cid.Cid) []byte
}

// BloomFilterManager provides unified bloom filter management across EAT implementations.
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
// This is intended for internal use by EAT implementations that need direct access.
func (bfm *BloomFilterManager) GetBloomCache() *bloom.BloomCache {
	return bfm.bloomCache
}

// BaseEAT provides common EAT construction and validation logic.
type BaseEAT struct {
	KV           kvstore.KVStore
	BloomManager *BloomFilterManager
	KeyBuilder   KeyBuilder
}

// ValidateConfig validates that required EAT components are properly configured.
func (be *BaseEAT) ValidateConfig() error {
	if be.KV == nil {
		return fmt.Errorf("KVStore is required")
	}
	if be.KeyBuilder == nil {
		return fmt.Errorf("KeyBuilder is required")
	}
	return nil
}

// Helper function for common key generation patterns

// DefaultArcKey generates the standard arc key format.
// Used by overwrite and versioned EAT implementations.
func DefaultArcKey(bucketId, path string) []byte {
	// Format: bucketId:path
	return []byte(bucketId + ":" + path)
}

// VersionedArcKey generates a versioned arc key format.
// Used by versioned EAT implementation to include version information.
func VersionedArcKey(bucketId string, version cid.Cid, path string) []byte {
	// Format: bucketId:version:path
	return []byte(bucketId + ":" + version.String() + ":" + path)
}

// DefaultBucketPrefix generates the standard bucket prefix format.
// Used by overwrite EAT implementation.
func DefaultBucketPrefix(bucketId string) []byte {
	// Format: bucketId:
	return []byte(bucketId + ":")
}

// VersionedBucketPrefix generates a versioned bucket prefix format.
// Used by versioned EAT implementation to include version information.
func VersionedBucketPrefix(bucketId string, version cid.Cid) []byte {
	// Format: bucketId:version:
	return []byte(bucketId + ":" + version.String() + ":")
}

// RootKeyFormat generates the key for root->bucketId mapping.
// This is shared across all EAT implementations.
func RootKeyFormat(root cid.Cid) []byte {
	// Format: root:{cid}
	return []byte("root:" + root.String())
}
