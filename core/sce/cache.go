package sce

import (
	"container/list"
	"sync"
)

const DefaultCacheSize = 1024

// CacheOption configures the SCE session cache.
type CacheOption func(*cacheConfig)

type cacheConfig struct {
	maxSize int
}

func defaultCacheConfig() *cacheConfig {
	return &cacheConfig{maxSize: DefaultCacheSize}
}

// WithCacheSize sets the maximum number of cached sessions.
func WithCacheSize(n int) CacheOption {
	return func(c *cacheConfig) {
		if n > 0 {
			c.maxSize = n
		}
	}
}

// lruCache is a bounded LRU cache for SCE sessions.
type lruCache struct {
	mu    sync.Mutex
	max   int
	items map[string]*list.Element // key -> *listElement
	order *list.List               // front = most recently used
}

type cacheElement struct {
	key     string
	session *session
}

func newLRUCache(maxSize int) *lruCache {
	return &lruCache{
		max:   maxSize,
		items: make(map[string]*list.Element),
		order: list.New(),
	}
}

func (c *lruCache) Get(key string) (*session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(elem)
	return elem.Value.(*cacheElement).session, true
}

func (c *lruCache) Put(key string, s *session) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		elem.Value.(*cacheElement).session = s
		c.order.MoveToFront(elem)
		return
	}

	if c.order.Len() >= c.max {
		c.evict()
	}

	elem := c.order.PushFront(&cacheElement{key: key, session: s})
	c.items[key] = elem
}

func (c *lruCache) evict() {
	elem := c.order.Back()
	if elem == nil {
		return
	}
	c.order.Remove(elem)
	delete(c.items, elem.Value.(*cacheElement).key)
}

func (c *lruCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
