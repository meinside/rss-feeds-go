package rf

import (
	"log"
	"os"
	"time"

	"github.com/mmcdole/gofeed"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

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
	v(c.verbose, "dbCache - checking existence of cached item with guid: %s", guid)

	err := c.db.Model(&CachedItem{}).Where("guid = ?", guid).Select("count(*) > 0").Find(&exists).Error
	if err == nil {
		return exists
	}

	log.Printf("failed to check existence of cached item with guid '%s': %s", guid, err)

	return false
}

// Save saves given item to the cache.
func (c *dbCache) Save(item gofeed.Item, title, summary string) {
	v(c.verbose, "dbCache - saving item to cache: %s (%s)", item.Title, title)

	cached := CachedItem{
		Title: title,

		GUID:        item.GUID,
		Description: item.Description,

		Summary: summary,

		MarkedAsRead: false,
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

	err := c.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "guid"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title",
			"summary",
		}),
	}).Create(&cached).Error
	if err != nil {
		log.Printf("failed to upsert cached item: %s", err)
	}
}

// Fetch fetches the cached item with given `guid`.
func (c *dbCache) Fetch(guid string) *CachedItem {
	v(c.verbose, "dbCache - fetching cached item with guid: %s", guid)

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
	v(c.verbose, "dbCache - marking cached item with guid: %s as read", guid)

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
	v(c.verbose, "dbCache - listing cached items with includeItemsMarkedAsRead = %v", includeItemsMarkedAsRead)

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
	v(c.verbose, "dbCache - deleting cached items older than 1 month")

	result := c.db.Where("created_at < ?", time.Now().Add(-30*24*time.Hour)).Delete(&CachedItem{})
	if result.Error != nil {
		log.Printf("failed to delete cached items older than 1 month: %s", result.Error)
	} else if result.RowsAffected > 0 {
		v(c.verbose, "dbCache - deleted %d cached items", result.RowsAffected)
	}
}

// SetVerbose sets the verbosity of cache.
func (c *dbCache) SetVerbose(v bool) {
	c.verbose = v
}

// return a new db cache
func newDBCache(filepath string) (cache *dbCache, err error) {
	if db, err := gorm.Open(sqlite.Open(filepath), &gorm.Config{
		Logger: logger.New(
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             slowQueryThresholdSeconds * time.Second,
				LogLevel:                  logger.Warn,
				IgnoreRecordNotFoundError: true,
				ParameterizedQueries:      true,
				Colorful:                  false,
			},
		),
	}); err == nil {
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
