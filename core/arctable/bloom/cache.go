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

// BucketMeta holds metadata for a bucket.
type BucketMeta struct {
	Config        BucketConfig `json:"config"`
	CreatedAt     time.Time    `json:"createdAt"`
	LastUpdatedAt time.Time    `json:"lastUpdatedAt,omitempty"`
}

// BucketConfig holds bloom filter configuration for a bucket.
type BucketConfig struct {
	ExpectedItems     int     `json:"expectedItems"`
	FalsePositiveRate float64 `json:"falsePositiveRate"`
}

// DefaultBucketConfig returns the default bucket configuration.
func DefaultBucketConfig() *BucketConfig {
	return &BucketConfig{
		ExpectedItems:     DefaultExpectedItems,
		FalsePositiveRate: DefaultFalsePositiveRate,
	}
}

// cacheEntry holds a cached bloom filter.
type cacheEntry struct {
	key    string
	filter *StandardBloom
}

// BloomCache is an independent component that manages bloom filters for all buckets.
// It provides LRU caching and persistence via KVStore.
type BloomCache struct {
	kv      kvstore.KVStore
	maxSize int

	mu       sync.RWMutex
	items    map[string]*list.Element
	eviction *list.List

	// Default config for new buckets
	defaultConfig *BucketConfig
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
		defaultConfig: DefaultBucketConfig(),
	}
}

// NewBloomCacheWithConfig creates a new BloomCache with custom default config.
func NewBloomCacheWithConfig(kv kvstore.KVStore, maxSize int, defaultConfig *BucketConfig) *BloomCache {
	if maxSize <= 0 {
		maxSize = 100
	}
	if defaultConfig == nil {
		defaultConfig = DefaultBucketConfig()
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

// bucketMetaKey returns the KVStore key for bucket metadata.
func bucketMetaKey(bucketId string) []byte {
	return []byte("bucket:" + bucketId + ":meta")
}

// bucketBloomKey returns the KVStore key for bucket bloom filter.
func bucketBloomKey(bucketId string) []byte {
	return []byte("bucket:" + bucketId + ":bloom")
}

// GetBucketMeta retrieves bucket metadata from KVStore.
func (bc *BloomCache) GetBucketMeta(ctx context.Context, bucketId string) (*BucketMeta, error) {
	key := bucketMetaKey(bucketId)
	data, err := bc.kv.Get(ctx, key)
	if err == kvstore.ErrNotFound {
		return nil, nil // Not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get bucket meta: %w", err)
	}

	var meta BucketMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to unmarshal bucket meta: %w", err)
	}

	return &meta, nil
}

// CreateBucket creates a new bucket with the given configuration.
// It persists the bucket metadata and initializes an empty bloom filter.
func (bc *BloomCache) CreateBucket(ctx context.Context, bucketId string, cfg *BucketConfig) error {
	if cfg == nil {
		cfg = bc.defaultConfig
	}

	// Check if bucket already exists
	if meta, err := bc.GetBucketMeta(ctx, bucketId); err != nil {
		return fmt.Errorf("failed to check bucket existence: %w", err)
	} else if meta != nil {
		return fmt.Errorf("bucket %s already exists", bucketId)
	}

	// Create bucket metadata
	meta := &BucketMeta{
		Config:    *cfg,
		CreatedAt: time.Now(),
	}

	// Persist metadata
	metaData, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal bucket meta: %w", err)
	}

	batch := bc.kv.Batch()
	if err := batch.Put(bucketMetaKey(bucketId), metaData); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to put bucket meta: %w", err)
	}

	// Initialize empty bloom filter
	filter := NewStandardBloom(cfg.ExpectedItems, cfg.FalsePositiveRate)
	bloomData, err := filter.MarshalBinary()
	if err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to marshal bloom filter: %w", err)
	}

	if err := batch.Put(bucketBloomKey(bucketId), bloomData); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to put bloom filter: %w", err)
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit bucket creation: %w", err)
	}

	// Cache the bloom filter
	bc.cacheSet(bucketId, filter)

	return nil
}

// DeleteBucket deletes a bucket and its bloom filter.
func (bc *BloomCache) DeleteBucket(ctx context.Context, bucketId string) error {
	batch := bc.kv.Batch()

	if err := batch.Delete(bucketMetaKey(bucketId)); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to delete bucket meta: %w", err)
	}

	if err := batch.Delete(bucketBloomKey(bucketId)); err != nil {
		batch.Cancel()
		return fmt.Errorf("failed to delete bloom filter: %w", err)
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit bucket deletion: %w", err)
	}

	// Remove from cache
	bc.cacheDelete(bucketId)

	return nil
}

// Get retrieves the bloom filter for a bucket.
// It first checks the cache, then loads from KVStore if not found.
// If the bucket doesn't exist, it creates one with default config.
func (bc *BloomCache) Get(ctx context.Context, bucketId string) (*StandardBloom, error) {
	// Check cache first
	if filter := bc.cacheGet(bucketId); filter != nil {
		return filter, nil
	}

	// Load from KVStore
	key := bucketBloomKey(bucketId)
	data, err := bc.kv.Get(ctx, key)
	if err == kvstore.ErrNotFound {
		// Bucket doesn't exist, create with default config
		if err := bc.CreateBucket(ctx, bucketId, nil); err != nil {
			return nil, fmt.Errorf("failed to create bucket: %w", err)
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
	bc.cacheSet(bucketId, filter)

	return filter, nil
}

// MightContain checks if a path might exist in a bucket using the bloom filter.
func (bc *BloomCache) MightContain(ctx context.Context, bucketId string, path string) (bool, error) {
	filter, err := bc.Get(ctx, bucketId)
	if err != nil {
		return true, err // On error, conservatively return true
	}
	return filter.Test([]byte(path)), nil
}

// MightContainBatch checks multiple paths at once using the bloom filter.
func (bc *BloomCache) MightContainBatch(ctx context.Context, bucketId string, paths []string) (map[string]bool, error) {
	filter, err := bc.Get(ctx, bucketId)
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

// Add adds paths to a bucket's bloom filter and persists it.
func (bc *BloomCache) Add(ctx context.Context, bucketId string, paths []string) error {
	filter, err := bc.Get(ctx, bucketId)
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

	key := bucketBloomKey(bucketId)
	if err := bc.kv.Put(ctx, key, bloomData); err != nil {
		return fmt.Errorf("failed to persist bloom filter: %w", err)
	}

	return nil
}

// UpdateBucketMeta updates the bucket metadata.
func (bc *BloomCache) UpdateBucketMeta(ctx context.Context, bucketId string, meta *BucketMeta) error {
	meta.LastUpdatedAt = time.Now()

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal bucket meta: %w", err)
	}

	key := bucketMetaKey(bucketId)
	if err := bc.kv.Put(ctx, key, data); err != nil {
		return fmt.Errorf("failed to persist bucket meta: %w", err)
	}

	return nil
}

// Invalidate removes a bucket's bloom filter from the cache.
func (bc *BloomCache) Invalidate(bucketId string) {
	bc.cacheDelete(bucketId)
}

// Clear removes all entries from the cache.
func (bc *BloomCache) Clear() {
	bc.cacheClear()
}

// Size returns the current number of entries in the cache.
func (bc *BloomCache) Size() int {
	return bc.cacheSize()
}

// DefaultConfig returns the default bucket configuration.
func (bc *BloomCache) DefaultConfig() *BucketConfig {
	return bc.defaultConfig
}
