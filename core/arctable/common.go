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
func (bfm *BloomFilterManager) MightContain(namespace, path string) bool {
	if bfm.bloomCache == nil {
		return true // Bloom disabled, assume it might exist
	}
	// Note: This is a simplified synchronous check; actual implementation
	// in BloomCache may be async. Callers should wrap async calls as needed.
	result, err := bfm.bloomCache.MightContain(nil, namespace, path)
	if err != nil {
		return true // On error, assume it might exist
	}
	return result
}

// CreateNamespace creates a new namespace with custom bloom configuration.
func (bfm *BloomFilterManager) CreateNamespace(ctx context.Context, namespace string, cfg *bloom.NamespaceConfig) error {
	if bfm.bloomCache == nil {
		return fmt.Errorf("bloom cache not configured")
	}
	return bfm.bloomCache.CreateNamespace(ctx, namespace, cfg)
}

// Insert records that a path exists in the bloom filter.
func (bfm *BloomFilterManager) Insert(ctx context.Context, namespace, path string) error {
	if bfm.bloomCache == nil {
		return nil // Bloom disabled
	}
	return bfm.bloomCache.Add(ctx, namespace, []string{path})
}

// Delete records that a path no longer exists in the bloom filter.
// Note: BloomCache does not support path deletion, only namespace-level operations.
// This is a no-op to maintain interface consistency.
func (bfm *BloomFilterManager) Delete(ctx context.Context, namespace, path string) error {
	if bfm.bloomCache == nil {
		return nil // Bloom disabled
	}
	// Bloom filter does not support single path deletion
	// Callers should invalidate the entire namespace if needed
	return nil
}

// Enabled returns whether bloom filter is enabled.
func (bfm *BloomFilterManager) Enabled() bool {
	return bfm.bloomCache != nil
}

// AddBatch adds multiple paths to the bloom filter at once.
func (bfm *BloomFilterManager) AddBatch(ctx context.Context, namespace string, paths []string) error {
	if bfm.bloomCache == nil {
		return nil
	}
	return bfm.bloomCache.Add(ctx, namespace, paths)
}

// MightContainBatch checks multiple paths at once using bloom filter.
func (bfm *BloomFilterManager) MightContainBatch(ctx context.Context, namespace string, paths []string) (map[string]bool, error) {
	if bfm.bloomCache == nil {
		result := make(map[string]bool)
		for _, p := range paths {
			result[p] = true
		}
		return result, nil
	}
	return bfm.bloomCache.MightContainBatch(ctx, namespace, paths)
}

// GetBloomCache returns the underlying BloomCache for advanced operations.
// This is intended for internal use by ArcTable implementations that need direct access.
func (bfm *BloomFilterManager) GetBloomCache() *bloom.BloomCache {
	return bfm.bloomCache
}

// Helper function for common key generation patterns

// DefaultArcKey generates the standard arc key format.
// Used by overwrite ArcTable implementation.
func DefaultArcKey(namespace string, path arcset.Path) []byte {
	// Format: namespace:path
	return []byte(namespace + ":" + path.String())
}

// DefaultNamespacePrefix generates the standard namespace prefix format.
// Used by overwrite ArcTable implementation.
func DefaultNamespacePrefix(namespace string) []byte {
	// Format: namespace:
	return []byte(namespace + ":")
}

// VersionedArcKey generates a versioned arc key format.
// Used by versioned ArcTable implementation to include version information.
func VersionedArcKey(namespace string, version cid.Cid, path arcset.Path) []byte {
	// Format: namespace:version:path
	return []byte(namespace + ":" + version.String() + ":" + path.String())
}

// VersionedNamespacePrefix generates a versioned namespace prefix format.
// Used by versioned ArcTable implementation to include version information.
func VersionedNamespacePrefix(namespace string, version cid.Cid) []byte {
	// Format: namespace:version:
	return []byte(namespace + ":" + version.String() + ":")
}

// RootKeyFormat generates the key for root->namespace mapping.
// This is shared across all ArcTable implementations.
func RootKeyFormat(root cid.Cid) []byte {
	// Format: root:{cid}
	return []byte("root:" + root.String())
}
