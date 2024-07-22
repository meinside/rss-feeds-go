package rf

import (
	"log"
	"maps"
	"slices"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/gorilla/feeds"
)

const (
	listLimit = 100
)

// FeedsItemsCache is an interface of feeds items' cache
type FeedsItemsCache interface {
	Exists(guid string) bool
	Save(item feeds.RssItem, summary string)
	Fetch(guid string) *CachedItem
	MarkAsRead(guid string)
	List(includeItemsMarkedAsRead bool) []CachedItem
	DeleteOlderThan1Month()

	SetVerbose(v bool)
}

// CachedItem is a struct for a cached item
type CachedItem struct {
	gorm.Model

	Title       string
	Link        string // url to the original article
	Comments    string // url to the community comments
	GUID        string `gorm:"uniqueIndex"`
	Author      string
	PublishDate string
	Description string

	Summary      string
	MarkedAsRead bool `gorm:"index"`
}

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
	if c.verbose {
		log.Printf("[verbose] memCache - checking existence of cached item with guid: %s", guid)
	}

	_, exists := c.items[guid]

	return exists
}

// Save saves `id` to the cache.
func (c *memCache) Save(item feeds.RssItem, summary string) {
	if c.verbose {
		log.Printf("[verbose] memCache - saving item to cache: %s", item.Title)
	}

	c.items[item.Guid.Id] = CachedItem{
		Title:       item.Title,
		Link:        item.Link,
		Comments:    item.Comments,
		GUID:        item.Guid.Id,
		Author:      item.Author,
		PublishDate: item.PubDate,
		Description: item.Description,

		Summary: summary,
	}
}

// Fetch fetches the cached item with given `guid`.
func (c *memCache) Fetch(guid string) *CachedItem {
	if c.verbose {
		log.Printf("[verbose] memCache - fetching cached item with guid: %s", guid)
	}

	if v, exists := c.items[guid]; exists {
		return &v
	}
	return nil
}

// MarkAsRead marks a cached item as read.
func (c *memCache) MarkAsRead(guid string) {
	if c.verbose {
		log.Printf("[verbose] memCache - marking cached item with guid: %s as read", guid)
	}

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
	if c.verbose {
		log.Printf("[verbose] memCache - listing cached items with includeItemsMarkedAsRead = %v", includeItemsMarkedAsRead)
	}

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
	if c.verbose {
		log.Printf("[verbose] memCache - deleting cached items older than 1 month")
	}

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

////////////////
//
// (DB cache)
//

// db cache
type dbCache struct {
	db *gorm.DB

	verbose bool
}

// Exists checks for the existence of `id` in the cache.
func (c *dbCache) Exists(guid string) (exists bool) {
	if c.verbose {
		log.Printf("[verbose] dbCache - checking existence of cached item with guid: %s", guid)
	}

	err := c.db.Model(&CachedItem{}).Where("guid = ?", guid).Select("count(*) > 0").Find(&exists).Error
	if err == nil {
		return exists
	}

	log.Printf("failed to check existence of cached item with guid '%s': %s", guid, err)

	return false
}

// Save saves `id` to the cache.
func (c *dbCache) Save(item feeds.RssItem, summary string) {
	if c.verbose {
		log.Printf("[verbose] dbCache - saving item to cache: %s", item.Title)
	}

	cached := CachedItem{
		Title:       item.Title,
		Link:        item.Link,
		Comments:    item.Comments,
		GUID:        item.Guid.Id,
		Author:      item.Author,
		PublishDate: item.PubDate,
		Description: item.Description,

		Summary:      summary,
		MarkedAsRead: false,
	}

	err := c.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "guid"}},
		DoUpdates: clause.AssignmentColumns([]string{"summary"}),
	}).Create(&cached).Error
	if err != nil {
		log.Printf("failed to upsert cached item: %s", err)
	}
}

// Fetch fetches the cached item with given `guid`.
func (c *dbCache) Fetch(guid string) *CachedItem {
	if c.verbose {
		log.Printf("[verbose] dbCache - fetching cached item with guid: %s", guid)
	}

	var cached CachedItem
	err := c.db.Limit(1).Model(&CachedItem{}).Find(&cached).Where("guid = ?", guid).Error
	if err != nil {
		log.Printf("failed to fetch cached item with guid '%s': %s", guid, err)
		return nil
	}
	return &cached
}

// MarkAsRead marks a cached item as read.
func (c *dbCache) MarkAsRead(guid string) {
	if c.verbose {
		log.Printf("[verbose] dbCache - marking cached item with guid: %s as read", guid)
	}

	result := c.db.Model(&CachedItem{}).Where("guid = ?", guid).Update("marked_as_read", true)
	if result.RowsAffected != 1 {
		log.Printf("failed to update cached item with guid '%s' (number of updated: %d)", guid, result.RowsAffected)
	}
	if result.Error != nil {
		log.Printf("failed to update cached item with guid '%s': %s", guid, result.Error)
	}
}

// List lists cached items.
//
// NOTE: when including items marked as read, the count will be limited to `listLimit`.
func (c *dbCache) List(includeItemsMarkedAsRead bool) (items []CachedItem) {
	if c.verbose {
		log.Printf("[verbose] dbCache - listing cached items with includeItemsMarkedAsRead = %v", includeItemsMarkedAsRead)
	}

	tx := c.db.Model(&CachedItem{})
	if !includeItemsMarkedAsRead {
		tx = tx.Where("marked_as_read = ?", false).Order("created_at DESC")
	} else {
		tx = tx.Order("created_at DESC").Limit(listLimit)
	}

	err := tx.Find(&items).Error
	if err != nil {
		log.Printf("failed to list cached items: %s", err)
		return nil
	}

	return items
}

// DeleteOlderThan1Month deletes cached items which are older than 1 month.
func (c *dbCache) DeleteOlderThan1Month() {
	if c.verbose {
		log.Printf("[verbose] dbCache - deleting cached items older than 1 month")
	}

	result := c.db.Where("created_at < ?", time.Now().Add(-30*24*time.Hour)).Delete(&CachedItem{})
	if result.Error != nil {
		log.Printf("failed to delete cached items older than 1 month: %s", result.Error)
	} else if result.RowsAffected > 0 {
		log.Printf("[verbose] dbCache - deleted %d cached items", result.RowsAffected)
	}
}

// SetVerbose sets the verbosity of cache.
func (c *dbCache) SetVerbose(v bool) {
	c.verbose = v
}

// return a new db cache
func newDBCache(filepath string) (cache *dbCache, err error) {
	if db, err := gorm.Open(sqlite.Open(filepath), &gorm.Config{}); err == nil {
		// migrate the schema
		if err := db.AutoMigrate(&CachedItem{}); err != nil {
			log.Printf("failed to migrate db: %s", err)
		}

		return &dbCache{
			db: db,
		}, nil
	}

	return nil, err
}
