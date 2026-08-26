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

	cooldowns map[[2]string]CachedCooldown

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
func (c *memCache) Save(item gofeed.Item, title, summary string) error {
	v(c.verbose, "memCache - saving item to cache: %s (%s)", item.Title, title)

	c.mu.Lock()
	defer c.mu.Unlock()

	cached := newCachedItem(item, title, summary)
	c.items[item.GUID] = cached

	return nil
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
func (c *memCache) MarkAsRead(guid string) error {
	v(c.verbose, "memCache - marking cached item with guid: %s as read", guid)

	c.mu.Lock()
	defer c.mu.Unlock()

	if item, exists := c.items[guid]; exists {
		item.MarkedAsRead = true
		c.items[guid] = item
	}

	return nil
}

// List lists all cached items.
func (c *memCache) List(includeItemsMarkedAsRead bool) []CachedItem {
	v(c.verbose, "memCache - listing cached items with includeItemsMarkedAsRead = %v", includeItemsMarkedAsRead)

	c.mu.RLock()
	defer c.mu.RUnlock()

	var all []CachedItem
	for _, item := range c.items {
		if includeItemsMarkedAsRead || !item.MarkedAsRead {
			all = append(all, item)
		}
	}

	return all
}

// DeleteOlderThan1Month deletes cached items which are older than 1 month, and
// cooldowns which expired long ago.
func (c *memCache) DeleteOlderThan1Month() error {
	v(c.verbose, "memCache - deleting cached items older than 1 month")

	c.mu.Lock()
	defer c.mu.Unlock()

	maps.DeleteFunc(c.items, func(_ string, v CachedItem) bool {
		return v.CreatedAt.Before(time.Now().Add(-30 * 24 * time.Hour))
	})

	// NOTE: cooldowns this old are ignored on load anyway (see
	// `restoreCooldownsLocked`), and their combos may not even exist anymore
	stale := time.Now().Add(-time.Duration(staleCooldownSeconds) * time.Second)
	maps.DeleteFunc(c.cooldowns, func(_ [2]string, v CachedCooldown) bool {
		return v.Until.Before(stale)
	})

	return nil
}

// LoadCooldowns lists all cooldowns in the cache.
func (c *memCache) LoadCooldowns() []CachedCooldown {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return slices.Collect(maps.Values(c.cooldowns))
}

// SaveCooldown saves given cooldown to the cache.
func (c *memCache) SaveCooldown(cooldown CachedCooldown) error {
	v(c.verbose, "memCache - saving cooldown of model '%s' until: %s", cooldown.Model, cooldown.Until)

	c.mu.Lock()
	defer c.mu.Unlock()

	cooldown.UpdatedAt = time.Now()
	c.cooldowns[[2]string{cooldown.APIKeyHash, cooldown.Model}] = cooldown

	return nil
}

// DeleteCooldown deletes the cooldown of given (api key, model) from the cache.
func (c *memCache) DeleteCooldown(apiKeyHash, model string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.cooldowns, [2]string{apiKeyHash, model})

	return nil
}

// SetVerbose sets the verbosity of cache.
func (c *memCache) SetVerbose(v bool) {
	c.verbose = v
}

// return a new memory cache
func newMemCache() *memCache {
	return &memCache{
		items:     map[string]CachedItem{},
		cooldowns: map[[2]string]CachedCooldown{},
	}
}
