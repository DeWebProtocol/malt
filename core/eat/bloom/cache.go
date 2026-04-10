// Package bloom provides Bloom Filter implementations for EAT.
package bloom

import (
	"container/list"
	"sync"
)

// Cache is an LRU cache for bloom filters.
type Cache struct {
	mu       sync.RWMutex
	maxSize  int
	items    map[string]*list.Element
	eviction *list.List
}

// cacheEntry holds a cached bloom filter.
type cacheEntry struct {
	key    string
	filter BloomFilter
}

// NewCache creates a new LRU cache with the given max size.
func NewCache(maxSize int) *Cache {
	if maxSize <= 0 {
		maxSize = 100 // default
	}
	return &Cache{
		maxSize:  maxSize,
		items:    make(map[string]*list.Element),
		eviction: list.New(),
	}
}

// Get retrieves a bloom filter from the cache.
// Returns nil if not found.
func (c *Cache) Get(key string) BloomFilter {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if elem, ok := c.items[key]; ok {
		c.eviction.MoveToFront(elem)
		return elem.Value.(*cacheEntry).filter
	}
	return nil
}

// Set adds a bloom filter to the cache.
// If the cache is full, the oldest entry is evicted.
func (c *Cache) Set(key string, filter BloomFilter) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry
	if elem, ok := c.items[key]; ok {
		elem.Value.(*cacheEntry).filter = filter
		c.eviction.MoveToFront(elem)
		return
	}

	// Add new entry
	entry := &cacheEntry{key: key, filter: filter}
	elem := c.eviction.PushFront(entry)
	c.items[key] = elem

	// Evict if over capacity
	for c.eviction.Len() > c.maxSize {
		oldest := c.eviction.Back()
		if oldest != nil {
			c.eviction.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
}

// Delete removes a bloom filter from the cache.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.eviction.Remove(elem)
		delete(c.items, key)
	}
}

// Clear removes all entries from the cache.
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*list.Element)
	c.eviction.Init()
}

// Size returns the current number of entries in the cache.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.eviction.Len()
}