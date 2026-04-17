// Package commitment provides commitment scheme utilities and base implementations.
// This file contains the BaseScheme and CacheManager for reducing duplication
// across different commitment backends (KZG, IPA, Verkle).
package commitment

import (
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// CacheConfig defines the caching strategy for a commitment scheme.
type CacheConfig struct {
	// MaxEntries is the maximum number of cached commitments.
	// Set to 0 to disable caching.
	MaxEntries int
	// EvictPolicy defines the eviction strategy: "lru" (default), "lfu", "none"
	EvictPolicy string
}

// CacheEntry holds cached arc data for a commitment.
type CacheEntry struct {
	Paths  []string
	Values []cid.Cid
}

// CacheManager provides a thread-safe LRU cache for commitments.
// All implementations (KZG, IPA, Verkle) use this common cache.
type CacheManager struct {
	mu         sync.RWMutex
	cache      map[string]*CacheEntry
	order      []string // tracks insertion order for LRU eviction
	maxEntries int
}

// NewCacheManager creates a new cache manager.
func NewCacheManager(maxEntries int) *CacheManager {
	if maxEntries <= 0 {
		maxEntries = 1024 // default
	}
	return &CacheManager{
		cache:      make(map[string]*CacheEntry),
		order:      make([]string, 0, maxEntries),
		maxEntries: maxEntries,
	}
}

// Get retrieves a cache entry by key.
func (cm *CacheManager) Get(key string) (*CacheEntry, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	entry, ok := cm.cache[key]
	return entry, ok
}

// Set stores a cache entry and performs LRU eviction if needed.
func (cm *CacheManager) Set(key string, entry *CacheEntry) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// If key already exists, remove it to update position
	if _, exists := cm.cache[key]; exists {
		cm.cache[key] = entry
		return
	}

	// Evict if necessary
	cm.evictLockedIfNeeded()

	// Insert new entry
	cm.cache[key] = entry
	cm.order = append(cm.order, key)
}

// evictLockedIfNeeded removes the oldest half of the cache when capacity is exceeded.
// Must be called with cm.mu held.
func (cm *CacheManager) evictLockedIfNeeded() {
	if len(cm.cache) < cm.maxEntries {
		return
	}
	// Evict oldest half
	evictCount := cm.maxEntries / 2
	for i := 0; i < evictCount && len(cm.order) > 0; i++ {
		key := cm.order[0]
		cm.order = cm.order[1:]
		delete(cm.cache, key)
	}
}

// Size returns the current number of cached entries.
func (cm *CacheManager) Size() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.cache)
}

// Clear removes all entries from the cache.
func (cm *CacheManager) Clear() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cache = make(map[string]*CacheEntry)
	cm.order = make([]string, 0, cm.maxEntries)
}

// BaseScheme provides common batch and aggregate proof implementations
// across all commitment backends. Backends only need to implement the core
// Commit, Prove, Verify, and Update methods.
type BaseScheme struct {
	Cache *CacheManager
}

// NewBaseScheme creates a new base scheme with the given cache manager.
func NewBaseScheme(cache *CacheManager) *BaseScheme {
	if cache == nil {
		cache = NewCacheManager(1024)
	}
	return &BaseScheme{Cache: cache}
}

// Backend is the interface that commitment backends must implement.
// The base scheme provides batch and aggregate implementations on top of these.
type Backend interface {
	// Commit generates a commitment to an arc set and returns the commitment CID.
	Commit(arcs arcset.ArcSet) (cid.Cid, error)

	// ProveSingle generates a single proof for a path.
	// Returns the target CID and proof bytes.
	ProveSingle(commitment cid.Cid, arcs arcset.ArcSet, path string) (cid.Cid, []byte, error)

	// VerifySingle verifies a single proof.
	VerifySingle(commitment cid.Cid, path string, value cid.Cid, proof []byte) (bool, error)

	// Update updates a single value in the commitment and returns the new commitment CID.
	Update(commitment cid.Cid, arcs arcset.ArcSet, path string, oldValue, newValue cid.Cid) (cid.Cid, error)
}

// BatchProveImpl generates proofs for multiple paths using the provided prover function.
// This is a common implementation used by all backends.
func (bs *BaseScheme) BatchProveImpl(
	commitment cid.Cid,
	arcs arcset.ArcSet,
	backend Backend,
	paths []string,
) (map[string]arcset.BatchProofEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	results := make(map[string]arcset.BatchProofEntry, len(paths))

	for _, path := range paths {
		target, proof, err := backend.ProveSingle(commitment, arcs, path)
		if err != nil {
			return nil, fmt.Errorf("failed to prove path %s: %w", path, err)
		}

		results[path] = arcset.BatchProofEntry{
			Target: target,
			Proof:  proof,
		}
	}

	return results, nil
}

// BatchVerifyImpl verifies multiple proofs using the provided verifier function.
// This is a common implementation used by all backends.
func (bs *BaseScheme) BatchVerifyImpl(
	commitment cid.Cid,
	backend Backend,
	proofs map[string]arcset.BatchProofEntry,
) (bool, error) {
	for path, entry := range proofs {
		ok, err := backend.VerifySingle(commitment, path, entry.Target, entry.Proof)
		if err != nil {
			return false, fmt.Errorf("failed to verify path %s: %w", path, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// AggregateProveImpl generates an aggregated proof for multiple paths.
// This is a common implementation used by all backends.
// It strips the index from individual proofs since paths are already stored.
func (bs *BaseScheme) AggregateProveImpl(
	commitment cid.Cid,
	arcs arcset.ArcSet,
	backend Backend,
	paths []string,
	proofSize int, // total size of proof including index
) (*arcset.AggregatedProof, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	// For KZG-style backends, proofSize is 84 (48 + 32 + 4)
	// but we only store 80 bytes (48 + 32) in aggregated proofs
	// since the index can be reconstructed from the paths array
	coreProofSize := proofSize - 4 // remove index

	targets := make([]cid.Cid, len(paths))
	proofData := make([]byte, 0, len(paths)*coreProofSize)

	for i, path := range paths {
		target, proof, err := backend.ProveSingle(commitment, arcs, path)
		if err != nil {
			return nil, fmt.Errorf("failed to prove path %s: %w", path, err)
		}

		targets[i] = target
		// Strip the last 4 bytes (index) from the proof
		proofData = append(proofData, proof[:len(proof)-4]...)
	}

	return &arcset.AggregatedProof{
		Paths:     paths,
		Targets:   targets,
		ProofData: proofData,
	}, nil
}

// AggregateVerifyImpl verifies an aggregated proof.
// Note: Aggregated proofs store proofs without the index since paths are already stored.
// This method reconstructs the full proof with the original format for verification.
func (bs *BaseScheme) AggregateVerifyImpl(
	commitment cid.Cid,
	backend Backend,
	aggProof *arcset.AggregatedProof,
	proofSize int, // total size of single proof including index
) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}

	// For KZG-style backends, each individual proof is 80 bytes (48 + 32)
	// and we need to reconstruct the full 84-byte proof format (48 + 32 + 4 index bytes)
	coreProofSize := proofSize - 4
	if len(aggProof.ProofData) != len(aggProof.Paths)*coreProofSize {
		return false, fmt.Errorf("proof data size mismatch: expected %d, got %d",
			len(aggProof.Paths)*coreProofSize, len(aggProof.ProofData))
	}

	for i, path := range aggProof.Paths {
		offset := i * coreProofSize
		// Strip the index from this position for verification
		fullProof := make([]byte, 0, proofSize)
		fullProof = append(fullProof, aggProof.ProofData[offset:offset+coreProofSize]...)
		// Append index bytes (4 bytes representing the position)
		fullProof = append(fullProof, byte(i>>24), byte(i>>16), byte(i>>8), byte(i))

		ok, err := backend.VerifySingle(commitment, path, aggProof.Targets[i], fullProof)
		if err != nil {
			return false, fmt.Errorf("failed to verify path %s: %w", path, err)
		}
		if !ok {
			return false, nil
		}
	}

	return true, nil
}

// Helper functions for common utility operations

// FindPathIndex finds the index of a path in a sorted paths slice using binary search.
// This is used by all backends to locate paths efficiently.
func FindPathIndex(paths []string, path string) (int, bool) {
	low, high := 0, len(paths)-1
	for low <= high {
		mid := (low + high) / 2
		if paths[mid] == path {
			return mid, true
		}
		if paths[mid] < path {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return -1, false
}
