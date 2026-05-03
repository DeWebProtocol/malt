// Package bloom provides Bloom Filter implementations for ArcTable.
package bloom

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dewebprotocol/malt/core/kvstore"
)

// NamespaceMeta holds metadata for a namespace.
type NamespaceMeta struct {
	Config        NamespaceConfig `json:"config"`
	CreatedAt     time.Time       `json:"createdAt"`
	LastUpdatedAt time.Time       `json:"lastUpdatedAt,omitempty"`
}

// NamespaceConfig holds bloom filter configuration for a namespace.
type NamespaceConfig struct {
	ExpectedItems     int     `json:"expectedItems"`
	FalsePositiveRate float64 `json:"falsePositiveRate"`
}

// DefaultNamespaceConfig returns the default namespace configuration.
func DefaultNamespaceConfig() *NamespaceConfig {
	return &NamespaceConfig{
		ExpectedItems:     DefaultExpectedItems,
		FalsePositiveRate: DefaultFalsePositiveRate,
	}
}

// cacheEntry holds a cached bloom filter.
type cacheEntry struct {
	key    string
	filter *StandardBloom
}

// BloomCache is an independent component that manages bloom filters for all namespaces.
// It provides LRU caching and persistence via KVStore.
type BloomCache struct {
	kv      kvstore.KVStore
	maxSize int

	mu       sync.RWMutex
	items    map[string]*list.Element
	eviction *list.List

	// Default config for new namespaces
	defaultConfig *NamespaceConfig
}

// NewBloomCache creates a new BloomCache with the given KVStore and cache size.
func NewBloomCache(kv kvstore.KVStore, maxSize int) *BloomCache {
	if maxSize <= 0 {
		maxSize = 100
	}
	return &BloomCache{
		kv:            kv,
		maxSize:       maxSize,
		items:         make(map[string]*list.Element),
		eviction:      list.New(),
		defaultConfig: DefaultNamespaceConfig(),
	}
}

// NewBloomCacheWithConfig creates a new BloomCache with custom default config.
func NewBloomCacheWithConfig(kv kvstore.KVStore, maxSize int, defaultConfig *NamespaceConfig) *BloomCache {
	if maxSize <= 0 {
		maxSize = 100
	}
	if defaultConfig == nil {
		defaultConfig = DefaultNamespaceConfig()
	}
	return &BloomCache{
		kv:            kv,
		maxSize:       maxSize,
		items:         make(map[string]*list.Element),
		eviction:      list.New(),
		defaultConfig: defaultConfig,
	}
}

func (bc *BloomCache) cacheGet(key string) *StandardBloom {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if elem, ok := bc.items[key]; ok {
		bc.eviction.MoveToFront(elem)
		return elem.Value.(*cacheEntry).filter
	}
	return nil
}

func (bc *BloomCache) cacheSet(key string, filter *StandardBloom) {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if elem, ok := bc.items[key]; ok {
		elem.Value.(*cacheEntry).filter = filter
		bc.eviction.MoveToFront(elem)
		return
	}

	entry := &cacheEntry{key: key, filter: filter}
	elem := bc.eviction.PushFront(entry)
	bc.items[key] = elem

	for bc.eviction.Len() > bc.maxSize {
		oldest := bc.eviction.Back()
		if oldest != nil {
			bc.eviction.Remove(oldest)
			delete(bc.items, oldest.Value.(*cacheEntry).key)
		}
	}
}

func (bc *BloomCache) cacheDelete(key string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if elem, ok := bc.items[key]; ok {
		bc.eviction.Remove(elem)
		delete(bc.items, key)
	}
}

func (bc *BloomCache) cacheClear() {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.items = make(map[string]*list.Element)
	bc.eviction.Init()
}

func (bc *BloomCache) cacheSize() int {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.eviction.Len()
}

// namespaceMetaKey returns the KVStore key for namespace metadata.
func namespaceMetaKey(namespace string) []byte {
	return []byte("namespace:" + namespace + ":meta")
}

// namespaceBloomKey returns the KVStore key for namespace bloom filter.
func namespaceBloomKey(namespace string) []byte {
	return []byte("namespace:" + namespace + ":bloom")
}

// GetNamespaceMeta retrieves namespace metadata from KVStore.
func (bc *BloomCache) GetNamespaceMeta(ctx context.Context, namespace string) (*NamespaceMeta, error) {
	key := namespaceMetaKey(namespace)
	data, err := bc.kv.Get(ctx, key)
	if err == kvstore.ErrNotFound {
		return nil, nil // Not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace meta: %w", err)
	}

	var meta NamespaceMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal namespace meta: %w", err)
	}

	return &meta, nil
}

// CreateNamespace creates a new namespace with the given configuration.
// It persists the namespace metadata and initializes an empty bloom filter.
func (bc *BloomCache) CreateNamespace(ctx context.Context, namespace string, cfg *NamespaceConfig) error {
	if cfg == nil {
		cfg = bc.defaultConfig
	}

	// Check if namespace already exists
	if meta, err := bc.GetNamespaceMeta(ctx, namespace); err != nil {
		return fmt.Errorf("failed to check namespace existence: %w", err)
	} else if meta != nil {
		return fmt.Errorf("namespace %s already exists", namespace)
	}

	// Create namespace metadata
	meta := &NamespaceMeta{
		Config:    *cfg,
		CreatedAt: time.Now(),
	}

	// Persist metadata
	metaData, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal namespace meta: %w", err)
	}

	batch := bc.kv.Batch()
	if err := batch.Put(namespaceMetaKey(namespace), metaData); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to put namespace meta: %w", err)
	}

	// Initialize empty bloom filter
	filter := NewStandardBloom(cfg.ExpectedItems, cfg.FalsePositiveRate)
	bloomData, err := filter.MarshalBinary()
	if err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to marshal bloom filter: %w", err)
	}

	if err := batch.Put(namespaceBloomKey(namespace), bloomData); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to put bloom filter: %w", err)
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit namespace creation: %w", err)
	}

	// Cache the bloom filter
	bc.cacheSet(namespace, filter)

	return nil
}

// DeleteNamespace deletes a namespace and its bloom filter.
func (bc *BloomCache) DeleteNamespace(ctx context.Context, namespace string) error {
	batch := bc.kv.Batch()

	if err := batch.Delete(namespaceMetaKey(namespace)); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to delete namespace meta: %w", err)
	}

	if err := batch.Delete(namespaceBloomKey(namespace)); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to delete bloom filter: %w", err)
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit namespace deletion: %w", err)
	}

	// Remove from cache
	bc.cacheDelete(namespace)

	return nil
}

// Get retrieves the bloom filter for a namespace.
// It first checks the cache, then loads from KVStore if not found.
// If the namespace doesn't exist, it creates one with default config.
func (bc *BloomCache) Get(ctx context.Context, namespace string) (*StandardBloom, error) {
	// Check cache first
	if filter := bc.cacheGet(namespace); filter != nil {
		return filter, nil
	}

	// Load from KVStore
	key := namespaceBloomKey(namespace)
	data, err := bc.kv.Get(ctx, key)
	if err == kvstore.ErrNotFound {
		// Namespace doesn't exist, create with default config
		if err := bc.CreateNamespace(ctx, namespace, nil); err != nil {
			return nil, fmt.Errorf("failed to create namespace: %w", err)
		}
		// Try loading again
		data, err = bc.kv.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("failed to load bloom after creation: %w", err)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load bloom filter: %w", err)
	}

	// Deserialize
	filter := &StandardBloom{}
	if err := filter.UnmarshalBinary(data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bloom filter: %w", err)
	}

	// Cache it
	bc.cacheSet(namespace, filter)

	return filter, nil
}

// MightContain checks if a path might exist in a namespace using the bloom filter.
func (bc *BloomCache) MightContain(ctx context.Context, namespace string, path string) (bool, error) {
	filter, err := bc.Get(ctx, namespace)
	if err != nil {
		return true, err // On error, conservatively return true
	}
	return filter.Test([]byte(path)), nil
}

// MightContainBatch checks multiple paths at once using the bloom filter.
func (bc *BloomCache) MightContainBatch(ctx context.Context, namespace string, paths []string) (map[string]bool, error) {
	filter, err := bc.Get(ctx, namespace)
	if err != nil {
		result := make(map[string]bool, len(paths))
		for _, p := range paths {
			result[p] = true
		}
		return result, err
	}

	result := make(map[string]bool, len(paths))
	for _, p := range paths {
		result[p] = filter.Test([]byte(p))
	}
	return result, nil
}

// Add adds paths to a namespace's bloom filter and persists it.
func (bc *BloomCache) Add(ctx context.Context, namespace string, paths []string) error {
	filter, err := bc.Get(ctx, namespace)
	if err != nil {
		return err
	}

	// Add paths to bloom
	for _, path := range paths {
		filter.Add([]byte(path))
	}

	// Persist
	bloomData, err := filter.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal bloom filter: %w", err)
	}

	key := namespaceBloomKey(namespace)
	if err := bc.kv.Put(ctx, key, bloomData); err != nil {
		return fmt.Errorf("failed to persist bloom filter: %w", err)
	}

	return nil
}

// UpdateNamespaceMeta updates the namespace metadata.
func (bc *BloomCache) UpdateNamespaceMeta(ctx context.Context, namespace string, meta *NamespaceMeta) error {
	meta.LastUpdatedAt = time.Now()

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal namespace meta: %w", err)
	}

	key := namespaceMetaKey(namespace)
	if err := bc.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("failed to persist namespace meta: %w", err)
	}

	return nil
}

// Invalidate removes a namespace's bloom filter from the cache.
func (bc *BloomCache) Invalidate(namespace string) {
	bc.cacheDelete(namespace)
}

// Clear removes all entries from the cache.
func (bc *BloomCache) Clear() {
	bc.cacheClear()
}

// Size returns the current number of entries in the cache.
func (bc *BloomCache) Size() int {
	return bc.cacheSize()
}

// DefaultConfig returns the default namespace configuration.
func (bc *BloomCache) DefaultConfig() *NamespaceConfig {
	return bc.defaultConfig
}
