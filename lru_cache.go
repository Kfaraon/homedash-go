package main

import (
	"container/list"
	"sync"
	"time"
)

// LRUCache implements a thread-safe Least Recently Used cache with TTL support
type LRUCache struct {
	capacity int
	ttl      time.Duration
	mu       sync.RWMutex
	cache    map[string]*list.Element
	lruList  *list.List
}

// cacheEntry represents a single entry in the LRU cache
type cacheEntry struct {
	key       string
	value     Status
	timestamp time.Time
}

// NewLRUCache creates a new LRU cache with the given capacity and TTL
// If capacity <= 0, the cache has unlimited capacity
// If ttl <= 0, entries never expire
func NewLRUCache(capacity int, ttl time.Duration) *LRUCache {
	return &LRUCache{
		capacity: capacity,
		ttl:      ttl,
		cache:    make(map[string]*list.Element),
		lruList:  list.New(),
	}
}

// Get retrieves a value from the cache
// Returns the value and true if found and not expired, false otherwise
func (c *LRUCache) Get(key string) (Status, bool) {
	c.mu.RLock()
	element, ok := c.cache[key]
	c.mu.RUnlock()

	if !ok {
		return Status{}, false
	}

	entry := element.Value.(*cacheEntry)
	
	// Check if entry has expired
	if c.ttl > 0 && time.Since(entry.timestamp) > c.ttl {
		c.mu.Lock()
		c.removeElement(element)
		c.mu.Unlock()
		return Status{}, false
	}

	// Move to front (most recently used)
	c.mu.Lock()
	c.lruList.MoveToFront(element)
	c.mu.Unlock()

	return entry.value, true
}

// Set adds or updates a value in the cache
func (c *LRUCache) Set(key string, value Status) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if key already exists
	if element, ok := c.cache[key]; ok {
		// Update existing entry
		entry := element.Value.(*cacheEntry)
		entry.value = value
		entry.timestamp = time.Now()
		c.lruList.MoveToFront(element)
		return
	}

	// Create new entry
	entry := &cacheEntry{
		key:       key,
		value:     value,
		timestamp: time.Now(),
	}

	element := c.lruList.PushFront(entry)
	c.cache[key] = element

	// Evict least recently used if over capacity
	if c.capacity > 0 && c.lruList.Len() > c.capacity {
		c.evictOldest()
	}
}

// Remove deletes a key from the cache
func (c *LRUCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.cache[key]; ok {
		c.removeElement(element)
	}
}

// RemoveExpired removes all expired entries from the cache
// Returns the number of entries removed
func (c *LRUCache) RemoveExpired() int {
	if c.ttl <= 0 {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	count := 0
	now := time.Now()
	var toRemove []*list.Element

	for element := c.lruList.Back(); element != nil; element = element.Prev() {
		entry := element.Value.(*cacheEntry)
		if now.Sub(entry.timestamp) > c.ttl {
			toRemove = append(toRemove, element)
		}
	}

	for _, element := range toRemove {
		c.removeElement(element)
		count++
	}

	return count
}

// Size returns the current number of entries in the cache
func (c *LRUCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lruList.Len()
}

// Clear removes all entries from the cache
func (c *LRUCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*list.Element)
	c.lruList.Init()
}

// Keys returns all keys in the cache (in order from most to least recently used)
func (c *LRUCache) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, c.lruList.Len())
	for element := c.lruList.Front(); element != nil; element = element.Next() {
		entry := element.Value.(*cacheEntry)
		keys = append(keys, entry.key)
	}
	return keys
}

// evictOldest removes the least recently used entry
func (c *LRUCache) evictOldest() {
	element := c.lruList.Back()
	if element != nil {
		c.removeElement(element)
	}
}

// removeElement removes a specific element from the cache
func (c *LRUCache) removeElement(element *list.Element) {
	entry := element.Value.(*cacheEntry)
	delete(c.cache, entry.key)
	c.lruList.Remove(element)
}

// LRUAppState extends AppState with LRU cache support
type LRUAppState struct {
	mu      sync.RWMutex
	groups  []Group
	lruCache *LRUCache
	stale   map[string]Status
	staleTS time.Time
}

// NewLRUAppState creates a new AppState with LRU cache
func NewLRUAppState(capacity int, ttl time.Duration) *LRUAppState {
	return &LRUAppState{
		lruCache: NewLRUCache(capacity, ttl),
		stale:    make(map[string]Status),
	}
}

// GetCache returns the current cache as a map
func (s *LRUAppState) GetCache() map[string]Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]Status)
	keys := s.lruCache.Keys()
	for _, key := range keys {
		if value, ok := s.lruCache.Get(key); ok {
			result[key] = value
		}
	}
	return result
}

// SetCache sets the cache from a map
func (s *LRUAppState) SetCache(cache map[string]Status) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear existing cache
	s.lruCache.Clear()
	
	// Add all entries
	for key, value := range cache {
		s.lruCache.Set(key, value)
	}
}

// GetStaleCache returns the stale cache if available
func (s *LRUAppState) GetStaleCache() (map[string]Status, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	
	if len(s.stale) == 0 {
		return nil, false
	}
	return s.stale, true
}

// SetStaleCache sets the stale cache
func (s *LRUAppState) SetStaleCache(cache map[string]Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	s.stale = cache
	s.staleTS = time.Now()
}