package rf

import (
	"gorm.io/gorm"

	"github.com/mmcdole/gofeed"
)

const (
	listLimit = 100

	slowQueryThresholdSeconds = 3
)

// FeedsItemsCache is an interface of feeds items' cache
type FeedsItemsCache interface {
	Exists(guid string) bool
	Save(item gofeed.Item, title, summary string)
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
