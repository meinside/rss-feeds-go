package rf

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

// test `newCachedItem`
func TestNewCachedItem(t *testing.T) {
	now := time.Now()

	t.Run("full item", func(t *testing.T) {
		item := gofeed.Item{
			GUID:        "guid-123",
			Title:       "Original Title",
			Description: "Original Description",
			Links:       []string{"https://example.com/article", "https://example.com/comments"},
			Author:      &gofeed.Person{Name: "Author Name", Email: "author@example.com"},
			PublishedParsed: &now,
		}

		cached := newCachedItem(item, "Translated Title", "Summary content")

		if cached.GUID != "guid-123" {
			t.Errorf("expected GUID 'guid-123', got %q", cached.GUID)
		}
		if cached.Title != "Translated Title" {
			t.Errorf("expected Title 'Translated Title', got %q", cached.Title)
		}
		if cached.Description != "Original Description" {
			t.Errorf("expected Description 'Original Description', got %q", cached.Description)
		}
		if cached.Summary != "Summary content" {
			t.Errorf("expected Summary 'Summary content', got %q", cached.Summary)
		}
		if cached.Link != "https://example.com/article" {
			t.Errorf("expected Link, got %q", cached.Link)
		}
		if cached.Comments != "https://example.com/comments" {
			t.Errorf("expected Comments, got %q", cached.Comments)
		}
		if cached.Author != "Author Name" {
			t.Errorf("expected Author 'Author Name', got %q", cached.Author)
		}
		if cached.PublishDate == "" {
			t.Error("expected PublishDate to be set")
		}
	})

	t.Run("nil author", func(t *testing.T) {
		item := gofeed.Item{
			GUID:   "guid-nil-author",
			Author: nil,
		}
		cached := newCachedItem(item, "title", "summary")
		if cached.Author != "" {
			t.Errorf("expected empty Author, got %q", cached.Author)
		}
	})

	t.Run("author with email only", func(t *testing.T) {
		item := gofeed.Item{
			GUID:   "guid-email-author",
			Author: &gofeed.Person{Email: "author@example.com"},
		}
		cached := newCachedItem(item, "title", "summary")
		if cached.Author != "author@example.com" {
			t.Errorf("expected Author 'author@example.com', got %q", cached.Author)
		}
	})

	t.Run("no links", func(t *testing.T) {
		item := gofeed.Item{
			GUID:  "guid-no-links",
			Links: nil,
		}
		cached := newCachedItem(item, "title", "summary")
		if cached.Link != "" {
			t.Errorf("expected empty Link, got %q", cached.Link)
		}
		if cached.Comments != "" {
			t.Errorf("expected empty Comments, got %q", cached.Comments)
		}
	})

	t.Run("single link", func(t *testing.T) {
		item := gofeed.Item{
			GUID:  "guid-single-link",
			Links: []string{"https://example.com/article"},
		}
		cached := newCachedItem(item, "title", "summary")
		if cached.Link != "https://example.com/article" {
			t.Errorf("expected Link, got %q", cached.Link)
		}
		if cached.Comments != "" {
			t.Errorf("expected empty Comments, got %q", cached.Comments)
		}
	})

	t.Run("nil PublishedParsed", func(t *testing.T) {
		item := gofeed.Item{
			GUID:            "guid-no-date",
			PublishedParsed: nil,
		}
		cached := newCachedItem(item, "title", "summary")
		if cached.PublishDate != "" {
			t.Errorf("expected empty PublishDate, got %q", cached.PublishDate)
		}
	})
}

// helper to create a test gofeed.Item
func testFeedItem(guid, title string) gofeed.Item {
	now := time.Now()
	return gofeed.Item{
		GUID:            guid,
		Title:           title,
		Description:     "desc",
		Links:           []string{"https://example.com/" + guid},
		PublishedParsed: &now,
	}
}

// test memCache operations
func TestMemCache(t *testing.T) {
	cache := newMemCache()

	t.Run("Exists returns false for missing item", func(t *testing.T) {
		if cache.Exists("nonexistent") {
			t.Error("expected false for nonexistent item")
		}
	})

	t.Run("Save and Exists", func(t *testing.T) {
		item := testFeedItem("guid-1", "Title 1")
		cache.Save(item, "Translated 1", "Summary 1")

		if !cache.Exists("guid-1") {
			t.Error("expected item to exist after Save")
		}
	})

	t.Run("Fetch", func(t *testing.T) {
		cached := cache.Fetch("guid-1")
		if cached == nil {
			t.Fatal("expected non-nil cached item")
		}
		if cached.Title != "Translated 1" {
			t.Errorf("expected Title 'Translated 1', got %q", cached.Title)
		}
		if cached.Summary != "Summary 1" {
			t.Errorf("expected Summary 'Summary 1', got %q", cached.Summary)
		}
	})

	t.Run("Fetch returns nil for missing item", func(t *testing.T) {
		if cache.Fetch("nonexistent") != nil {
			t.Error("expected nil for nonexistent item")
		}
	})

	t.Run("Save updates existing item", func(t *testing.T) {
		item := testFeedItem("guid-1", "Title 1 Updated")
		cache.Save(item, "Updated Title", "Updated Summary")

		cached := cache.Fetch("guid-1")
		if cached == nil {
			t.Fatal("expected non-nil cached item")
		}
		if cached.Title != "Updated Title" {
			t.Errorf("expected updated Title, got %q", cached.Title)
		}
	})

	t.Run("MarkAsRead", func(t *testing.T) {
		cache.MarkAsRead("guid-1")

		cached := cache.Fetch("guid-1")
		if cached == nil {
			t.Fatal("expected non-nil cached item")
		}
		if !cached.MarkedAsRead {
			t.Error("expected item to be marked as read")
		}
	})

	t.Run("MarkAsRead on nonexistent item", func(t *testing.T) {
		cache.MarkAsRead("nonexistent") // should not panic
	})

	t.Run("List without read items", func(t *testing.T) {
		item2 := testFeedItem("guid-2", "Title 2")
		cache.Save(item2, "Translated 2", "Summary 2")

		items := cache.List(false)
		for _, item := range items {
			if item.GUID == "guid-1" {
				t.Error("expected read item to be excluded")
			}
		}
		found := false
		for _, item := range items {
			if item.GUID == "guid-2" {
				found = true
			}
		}
		if !found {
			t.Error("expected unread item guid-2 to be in list")
		}
	})

	t.Run("List with read items", func(t *testing.T) {
		items := cache.List(true)
		if len(items) < 2 {
			t.Errorf("expected at least 2 items, got %d", len(items))
		}
	})

	t.Run("SetVerbose", func(t *testing.T) {
		cache.SetVerbose(true)
		cache.SetVerbose(false) // should not panic
	})

	t.Run("DeleteOlderThan1Month", func(t *testing.T) {
		// all items have zero CreatedAt (from gorm.Model), which is before 1 month ago
		cache.DeleteOlderThan1Month()

		items := cache.List(true)
		if len(items) != 0 {
			t.Errorf("expected all items deleted, got %d", len(items))
		}
	})
}

// test dbCache operations
func TestDBCache(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_cache.db")
	cache, err := newDBCache(dbPath)
	if err != nil {
		t.Fatalf("failed to create dbCache: %s", err)
	}

	t.Run("Exists returns false for missing item", func(t *testing.T) {
		if cache.Exists("nonexistent") {
			t.Error("expected false for nonexistent item")
		}
	})

	t.Run("Save and Exists", func(t *testing.T) {
		item := testFeedItem("db-guid-1", "DB Title 1")
		cache.Save(item, "DB Translated 1", "DB Summary 1")

		if !cache.Exists("db-guid-1") {
			t.Error("expected item to exist after Save")
		}
	})

	t.Run("Fetch", func(t *testing.T) {
		cached := cache.Fetch("db-guid-1")
		if cached == nil {
			t.Fatal("expected non-nil cached item")
		}
		if cached.Title != "DB Translated 1" {
			t.Errorf("expected Title 'DB Translated 1', got %q", cached.Title)
		}
		if cached.Summary != "DB Summary 1" {
			t.Errorf("expected Summary 'DB Summary 1', got %q", cached.Summary)
		}
	})

	t.Run("Fetch returns nil for missing item", func(t *testing.T) {
		if cache.Fetch("nonexistent") != nil {
			t.Error("expected nil for nonexistent item")
		}
	})

	t.Run("Save upserts existing item", func(t *testing.T) {
		item := testFeedItem("db-guid-1", "DB Title 1")
		cache.Save(item, "DB Updated Title", "DB Updated Summary")

		cached := cache.Fetch("db-guid-1")
		if cached == nil {
			t.Fatal("expected non-nil cached item")
		}
		if cached.Title != "DB Updated Title" {
			t.Errorf("expected updated Title, got %q", cached.Title)
		}
		if cached.Summary != "DB Updated Summary" {
			t.Errorf("expected updated Summary, got %q", cached.Summary)
		}
	})

	t.Run("MarkAsRead", func(t *testing.T) {
		cache.MarkAsRead("db-guid-1")

		cached := cache.Fetch("db-guid-1")
		if cached == nil {
			t.Fatal("expected non-nil cached item")
		}
		if !cached.MarkedAsRead {
			t.Error("expected item to be marked as read")
		}
	})

	t.Run("List without read items", func(t *testing.T) {
		item2 := testFeedItem("db-guid-2", "DB Title 2")
		cache.Save(item2, "DB Translated 2", "DB Summary 2")

		items := cache.List(false)
		for _, item := range items {
			if item.GUID == "db-guid-1" {
				t.Error("expected read item to be excluded")
			}
		}
		found := false
		for _, item := range items {
			if item.GUID == "db-guid-2" {
				found = true
			}
		}
		if !found {
			t.Error("expected unread item db-guid-2 to be in list")
		}
	})

	t.Run("List with read items", func(t *testing.T) {
		items := cache.List(true)
		if len(items) < 2 {
			t.Errorf("expected at least 2 items, got %d", len(items))
		}
	})

	t.Run("SetVerbose", func(t *testing.T) {
		cache.SetVerbose(true)
		cache.SetVerbose(false)
	})

	t.Run("DeleteOlderThan1Month", func(t *testing.T) {
		// recently created items should NOT be deleted
		cache.DeleteOlderThan1Month()

		items := cache.List(true)
		if len(items) < 2 {
			t.Errorf("expected items to remain (recently created), got %d", len(items))
		}
	})
}

