package rf

import (
	"maps"
	"slices"
	"time"

	"github.com/mmcdole/gofeed"
)

////////////////
//
// (memory cache)
//

// memory cache
type memCache struct {
	items map[string]CachedItem

	verbose bool
}

// Exists checks for the existence of `id` in the cache.
func (c *memCache) Exists(guid string) bool {
	v(c.verbose, "memCache - checking existence of cached item with guid: %s", guid)

	_, exists := c.items[guid]

	return exists
}

// Save saves given item to the cache.
func (c *memCache) Save(item gofeed.Item, title, summary string) {
	v(c.verbose, "memCache - saving item to cache: %s (%s)", item.Title, title)

	cached := CachedItem{
		Title: title,

		GUID:        item.GUID,
		Description: item.Description,

		Summary: summary,
	}
	if len(item.Links) > 0 {
		cached.Link = item.Links[0]
		if len(item.Links) > 1 {
			cached.Comments = item.Links[1]
		}
	}
	if item.Author != nil {
		if len(item.Author.Name) > 0 {
			cached.Author = item.Author.Name
		} else if len(item.Author.Email) > 0 {
			cached.Author = item.Author.Email
		}
	}
	if item.PublishedParsed != nil {
		cached.PublishDate = item.PublishedParsed.Format(time.RFC3339)
	}

	c.items[item.GUID] = cached
}

// Fetch fetches the cached item with given `guid`.
func (c *memCache) Fetch(guid string) *CachedItem {
	v(c.verbose, "memCache - fetching cached item with guid: %s", guid)

	if v, exists := c.items[guid]; exists {
		return &v
	}
	return nil
}

// MarkAsRead marks a cached item as read.
func (c *memCache) MarkAsRead(guid string) {
	v(c.verbose, "memCache - marking cached item with guid: %s as read", guid)

	if v, exists := c.items[guid]; exists {
		// overwrite it
		c.items[guid] = CachedItem{
			Title:        v.Title,
			Link:         v.Link,
			Comments:     v.Comments,
			GUID:         guid,
			Author:       v.Author,
			PublishDate:  v.PublishDate,
			Description:  v.Description,
			Summary:      v.Summary,
			MarkedAsRead: true,
		}
	}
}

// List lists all cached items.
func (c *memCache) List(includeItemsMarkedAsRead bool) []CachedItem {
	v(c.verbose, "memCache - listing cached items with includeItemsMarkedAsRead = %v", includeItemsMarkedAsRead)

	all := []CachedItem{}
	for _, v := range c.items {
		all = append(all, v)
	}

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
