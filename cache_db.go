package rf

import (
	"fmt"
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
func (c *dbCache) Save(item gofeed.Item, title, summary string) error {
	v(c.verbose, "dbCache - saving item to cache: %s (%s)", item.Title, title)

	cached := newCachedItem(item, title, summary)

	err := c.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "guid"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"title",
			"summary",
		}),
	}).Create(&cached).Error
	if err != nil {
		return fmt.Errorf("failed to upsert cached item '%s': %w", item.GUID, err)
	}

	return nil
}

// Fetch fetches the cached item with given `guid`.
func (c *dbCache) Fetch(guid string) *CachedItem {
	v(c.verbose, "dbCache - fetching cached item with guid: %s", guid)

	var cached CachedItem
	err := c.db.Where("guid = ?", guid).First(&cached).Error
	if err != nil {
		log.Printf("failed to fetch cached item with guid '%s': %s", guid, err)
		return nil
	}
	return &cached
}

// MarkAsRead marks a cached item as read.
func (c *dbCache) MarkAsRead(guid string) error {
	v(c.verbose, "dbCache - marking cached item with guid: %s as read", guid)

	result := c.db.Model(&CachedItem{}).Where("guid = ?", guid).Update("marked_as_read", true)
	if result.Error != nil {
		return fmt.Errorf("failed to mark cached item '%s' as read: %w", guid, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("unexpected rows affected when marking '%s' as read: %d", guid, result.RowsAffected)
	}

	return nil
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

// DeleteOlderThan1Month physically deletes cached items which are older than
// 1 month and cooldowns which expired long ago, then reclaims freed pages via
// incremental_vacuum.
func (c *dbCache) DeleteOlderThan1Month() error {
	v(c.verbose, "dbCache - deleting cached items older than 1 month")

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	result := c.db.Unscoped().Where("created_at < ?", cutoff).Delete(&CachedItem{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete cached items older than 1 month: %w", result.Error)
	}
	deleted := result.RowsAffected

	// NOTE: cooldowns this old are ignored on load anyway (see
	// `restoreCooldownsLocked`), and their combos may not even exist anymore
	stale := time.Now().Add(-time.Duration(staleCooldownSeconds) * time.Second)
	cooldowns := c.db.Where("until < ?", stale).Delete(&CachedCooldown{})
	if cooldowns.Error != nil {
		return fmt.Errorf("failed to delete stale cooldowns: %w", cooldowns.Error)
	}
	if cooldowns.RowsAffected > 0 {
		v(c.verbose, "dbCache - deleted %d stale cooldown(s)", cooldowns.RowsAffected)
	}
	deleted += cooldowns.RowsAffected

	if deleted > 0 {
		v(c.verbose, "dbCache - deleted %d cached item(s)", result.RowsAffected)

		// reclaim freed pages (non-fatal on failure; retried on next delete)
		if err := c.db.Exec("PRAGMA incremental_vacuum").Error; err != nil {
			v(c.verbose, "dbCache - incremental_vacuum failed: %s", err)
		}
	}

	return nil
}

// LoadCooldowns lists all persisted cooldowns.
func (c *dbCache) LoadCooldowns() (cooldowns []CachedCooldown) {
	v(c.verbose, "dbCache - loading persisted cooldowns")

	if err := c.db.Find(&cooldowns).Error; err != nil {
		log.Printf("failed to load persisted cooldowns: %s", err)
		return nil
	}

	return cooldowns
}

// SaveCooldown persists (or updates) given cooldown.
func (c *dbCache) SaveCooldown(cooldown CachedCooldown) error {
	v(c.verbose, "dbCache - saving cooldown of model '%s' until: %s", cooldown.Model, cooldown.Until)

	err := c.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "api_key_hash"}, {Name: "model"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"until",
			"failures",
			"updated_at",
		}),
	}).Create(&cooldown).Error
	if err != nil {
		return fmt.Errorf("failed to upsert cooldown of model '%s': %w", cooldown.Model, err)
	}

	return nil
}

// DeleteCooldown drops the persisted cooldown of given (api key, model).
func (c *dbCache) DeleteCooldown(apiKeyHash, model string) error {
	err := c.db.Where("api_key_hash = ? AND model = ?", apiKeyHash, model).
		Delete(&CachedCooldown{}).Error
	if err != nil {
		return fmt.Errorf("failed to delete cooldown of model '%s': %w", model, err)
	}

	return nil
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
		// enable incremental auto_vacuum before any table is created,
		// so that a newly created DB file reclaims freed pages on
		// `PRAGMA incremental_vacuum`. (existing files must be converted
		// once manually with `PRAGMA auto_vacuum=INCREMENTAL; VACUUM;`)
		if err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL").Error; err != nil {
			return nil, fmt.Errorf("failed to set auto_vacuum: %w", err)
		}

		// migrate the schema
		if err := db.AutoMigrate(&CachedItem{}, &CachedCooldown{}); err != nil {
			return nil, fmt.Errorf("failed to migrate db: %w", err)
		}

		return &dbCache{
			db: db,
		}, nil
	}

	return nil, err
}
