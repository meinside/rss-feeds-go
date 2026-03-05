package rf

import (
	"time"

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

// newCachedItem converts a gofeed.Item to a CachedItem.
func newCachedItem(item gofeed.Item, title, summary string) CachedItem {
	cached := CachedItem{
		Title:       title,
		GUID:        item.GUID,
		Description: item.Description,
		Summary:     summary,
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
	return cached
}
