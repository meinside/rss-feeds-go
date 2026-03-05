package rf

import (
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"
)

////////////////
//
// (memory cache)
//

// memory cache
type memCache struct {
	mu    sync.RWMutex
	items map[string]CachedItem

	verbose bool
}

// Exists checks for the existence of `id` in the cache.
func (c *memCache) Exists(guid string) bool {
	v(c.verbose, "memCache - checking existence of cached item with guid: %s", guid)

	c.mu.RLock()
	defer c.mu.RUnlock()

	_, exists := c.items[guid]

	return exists
}

// Save saves given item to the cache.
func (c *memCache) Save(item gofeed.Item, title, summary string) {
	v(c.verbose, "memCache - saving item to cache: %s (%s)", item.Title, title)

	c.mu.Lock()
	defer c.mu.Unlock()

	cached := newCachedItem(item, title, summary)
	c.items[item.GUID] = cached
}

// Fetch fetches the cached item with given `guid`.
func (c *memCache) Fetch(guid string) *CachedItem {
	v(c.verbose, "memCache - fetching cached item with guid: %s", guid)

	c.mu.RLock()
	defer c.mu.RUnlock()

	if v, exists := c.items[guid]; exists {
		return &v
	}
	return nil
}

// MarkAsRead marks a cached item as read.
func (c *memCache) MarkAsRead(guid string) {
	v(c.verbose, "memCache - marking cached item with guid: %s as read", guid)

	c.mu.Lock()
	defer c.mu.Unlock()

	if item, exists := c.items[guid]; exists {
		item.MarkedAsRead = true
		c.items[guid] = item
	}
}

// List lists all cached items.
func (c *memCache) List(includeItemsMarkedAsRead bool) []CachedItem {
	v(c.verbose, "memCache - listing cached items with includeItemsMarkedAsRead = %v", includeItemsMarkedAsRead)

	c.mu.RLock()
	all := []CachedItem{}
	for _, v := range c.items {
		all = append(all, v)
	}
	c.mu.RUnlock()

	return slices.DeleteFunc(all, func(v CachedItem) bool {
		if includeItemsMarkedAsRead {
			return false
		}
		return v.MarkedAsRead
	})
}

// DeleteOlderThan1Month deletes cached items which are older than 1 month.
func (c *memCache) DeleteOlderThan1Month() {
	v(c.verbose, "memCache - deleting cached items older than 1 month")

	c.mu.Lock()
	defer c.mu.Unlock()

	maps.DeleteFunc(c.items, func(_ string, v CachedItem) bool {
		return v.CreatedAt.Before(time.Now().Add(-30 * 24 * time.Hour))
	})
}

// SetVerbose sets the verbosity of cache.
func (c *memCache) SetVerbose(v bool) {
	c.verbose = v
}

// return a new memory cache
func newMemCache() *memCache {
	return &memCache{
		items: map[string]CachedItem{},
	}
}
