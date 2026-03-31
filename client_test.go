package rf

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// helper for creating a client with API key from env
func newTestClientWithAPI(t *testing.T) *Client {
	t.Helper()

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = defaultGoogleAIModel
	}

	client := NewClient([]string{apiKey}, nil)
	client.SetGoogleAIModels([]string{model})
	client.SetDesiredLanguage("ko_KR")

	return client
}

// test summarize content and translate title
func TestSummarizeURLContent(t *testing.T) {
	client := newTestClientWithAPI(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 60*time.Second)
	defer cancel()

	_, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		`meinside/rss-feeds-go: A go utility package for handling RSS feeds.`,
		`https://github.com/meinside/rss-feeds-go`,
	)
	if err != nil {
		t.Errorf("failed to summarize url content: %s", err)
	} else {
		log.Printf(">>> translated title: %s", translatedTitle)
		log.Printf(">>> summarized content: %s", summarizedContent)
	}
}

// test summarize url with url context
func TestSummarizeURL(t *testing.T) {
	client := newTestClientWithAPI(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 60*time.Second)
	defer cancel()

	_, translatedTitle, summarizedContent, err := client.summarizeURL(
		ctx,
		`meinside/gemini-things-go: A Golang library for generating things with Gemini APIs `,
		`https://github.com/meinside/gemini-things-go`,
		`ko_KR`,
	)
	if err != nil {
		t.Errorf("failed to summarize url: %s", err)
	} else {
		log.Printf(">>> [url context] translated title (untouched): %s", translatedTitle)
		log.Printf(">>> [url context] summarized content: %s", summarizedContent)
	}
}

// test summarize youtube url and translate title
func TestSummarizeYouTube(t *testing.T) {
	client := newTestClientWithAPI(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 60*time.Second)
	defer cancel()

	_, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		`I2C test on Raspberry Pi with Adafruit 8x8 LED Matrix and Ruby`,
		`https://www.youtube.com/watch?v=fV5rI_5fDI8`,
	)
	if err != nil {
		t.Errorf("failed to summarize youtube url: %s", err)
	} else {
		log.Printf(">>> [youtube] translated title: %s", translatedTitle)
		log.Printf(">>> [youtube] summarized content: %s", summarizedContent)
	}
}

// test summarize with wrong url (should succeed via gemini url context)
func TestSummarizeWrongURL(t *testing.T) {
	client := newTestClientWithAPI(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 60*time.Second)
	defer cancel()

	_, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		`What is the answer to life, the universe, and everything?`,
		`https://no-sucn-domain/that-will-lead/to/fetch-error`,
	)
	if err != nil {
		t.Errorf("should have failed with the wrong url")
	} else {
		if translatedTitle != `What is the answer to life, the universe, and everything?` {
			t.Errorf("should have kept the title, but got '%s'", translatedTitle)
		}
		log.Printf(">>> [wrong url] translated title (untouched): %s", translatedTitle)
		log.Printf(">>> [wrong url] summarized content (error message): %s", summarizedContent)
	}
}

// test summarize with wrong API key (should fail and keep original title)
func TestSummarizeWrongAPIKey(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	client := NewClient([]string{"intentionally-wrong-api-key"}, nil)
	client.SetGoogleAIModels([]string{defaultGoogleAIModel})
	client.SetDesiredLanguage("ko_KR")

	ctx, cancel := context.WithTimeout(context.TODO(), 60*time.Second)
	defer cancel()

	_, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		`What is the answer to life, the universe, and everything?`,
		`https://no-sucn-domain/that-will-lead/to/fetch-error`,
	)
	if err != nil {
		if translatedTitle != `What is the answer to life, the universe, and everything?` {
			t.Errorf("should have kept the title, but got '%s'", translatedTitle)
		}
		log.Printf(">>> [api error] translated title (untouched): %s", translatedTitle)
		log.Printf(">>> [api error] summarized content (api error): %s", summarizedContent)
	} else {
		t.Errorf("should have failed with the wrong api key")
	}
}

// test `NewClient`
func TestNewClient(t *testing.T) {
	client := NewClient([]string{"key1"}, []string{"https://example.com/feed"})
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.desiredLanguage != defaultDesiredLanguage {
		t.Errorf("expected default language %q, got %q", defaultDesiredLanguage, client.desiredLanguage)
	}
	if client.summarizeIntervalSeconds != defaultSummarizeIntervalSeconds {
		t.Errorf("expected default interval %d, got %d", defaultSummarizeIntervalSeconds, client.summarizeIntervalSeconds)
	}
}

// test `NewClientWithDB`
func TestNewClientWithDB(t *testing.T) {
	t.Run("valid path", func(t *testing.T) {
		dbPath := fmt.Sprintf("%s/test_client.db", t.TempDir())
		client, err := NewClientWithDB([]string{"key1"}, []string{"https://example.com/feed"}, dbPath)
		if err != nil {
			t.Fatalf("failed to create client with DB: %s", err)
		}
		if client == nil {
			t.Fatal("expected non-nil client")
		}
	})

}

// test setter methods
func TestClientSetters(t *testing.T) {
	client := NewClient([]string{"key1"}, nil)

	client.SetGoogleAIModels([]string{"model-a", "model-b"})
	if len(client.googleAIModels) != 2 {
		t.Errorf("expected 2 models, got %d", len(client.googleAIModels))
	}

	client.SetDesiredLanguage("Korean")
	if client.desiredLanguage != "Korean" {
		t.Errorf("expected 'Korean', got %q", client.desiredLanguage)
	}

	client.SetSummarizeIntervalSeconds(30)
	if client.summarizeIntervalSeconds != 30 {
		t.Errorf("expected 30, got %d", client.summarizeIntervalSeconds)
	}

	client.SetVerbose(true)
	if !client.verbose {
		t.Error("expected verbose to be true")
	}
	client.SetVerbose(false)
}

// test `rotatedAPIKeyAndModel`
func TestRotatedAPIKeyAndModel(t *testing.T) {
	client := NewClient([]string{"key-a", "key-b"}, nil)
	client.SetGoogleAIModels([]string{"model-x", "model-y", "model-z"})

	key0, model0 := client.rotatedAPIKeyAndModel()
	key1, model1 := client.rotatedAPIKeyAndModel()
	key2, model2 := client.rotatedAPIKeyAndModel()

	if key0 != "key-a" || key1 != "key-b" || key2 != "key-a" {
		t.Errorf("unexpected key rotation: %q, %q, %q", key0, key1, key2)
	}
	if model0 != "model-x" || model1 != "model-y" || model2 != "model-z" {
		t.Errorf("unexpected model rotation: %q, %q, %q", model0, model1, model2)
	}
}

// test `ListCachedItems` and `MarkCachedItemsAsRead`
func TestListAndMarkCachedItems(t *testing.T) {
	client := NewClient([]string{"secret-key"}, nil)

	// save items directly to cache
	item1 := testFeedItem("guid-list-1", "Title 1")
	item2 := testFeedItem("guid-list-2", "Title 2")
	_ = client.cache.Save(item1, "Title 1", "Summary with secret-key inside")
	_ = client.cache.Save(item2, "Title 2", "Clean summary")

	// list should redact API keys
	items := client.ListCachedItems(false)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for _, item := range items {
		if strings.Contains(item.Summary, "secret-key") {
			t.Errorf("expected API key to be redacted in summary: %q", item.Summary)
		}
	}

	// mark as read
	if err := client.MarkCachedItemsAsRead(items); err != nil {
		t.Fatalf("MarkCachedItemsAsRead failed: %s", err)
	}

	// list without read items should be empty
	items = client.ListCachedItems(false)
	if len(items) != 0 {
		t.Errorf("expected 0 unread items, got %d", len(items))
	}

	// list with read items should still have them
	items = client.ListCachedItems(true)
	if len(items) != 2 {
		t.Errorf("expected 2 items including read, got %d", len(items))
	}
}

// test `DeleteOldCachedItems`
func TestDeleteOldCachedItems(t *testing.T) {
	client := NewClient([]string{"key"}, nil)

	item := testFeedItem("guid-old", "Old Title")
	_ = client.cache.Save(item, "Old Title", "Old Summary")

	// memCache items have zero CreatedAt, so they are "old"
	if err := client.DeleteOldCachedItems(); err != nil {
		t.Fatalf("DeleteOldCachedItems failed: %s", err)
	}

	items := client.ListCachedItems(true)
	if len(items) != 0 {
		t.Errorf("expected 0 items after deleting old, got %d", len(items))
	}
}

// test `PublishXML`
func TestPublishXML(t *testing.T) {
	client := NewClient([]string{"key"}, nil)

	items := []CachedItem{
		{
			Title:       "Test Article",
			Link:        "https://example.com/article",
			GUID:        "guid-pub-1",
			Description: "A test article",
			Summary:     "This is a **summary** of the article.",
		},
		{
			Title:       "Article With Comments",
			Link:        "https://example.com/article2",
			Comments:    "https://example.com/comments",
			GUID:        "guid-pub-2",
			Description: "Another article",
			Summary:     "Summary of another article.",
		},
		{
			Title:       "No Summary",
			Link:        "https://example.com/article3",
			GUID:        "guid-pub-3",
			Description: "Article without summary",
			Summary:     "", // should be omitted
		},
	}

	bytes, err := client.PublishXML("Test Feed", "https://example.com", "Test Description", "Author", "email@example.com", items)
	if err != nil {
		t.Fatalf("PublishXML failed: %s", err)
	}

	xmlStr := string(bytes)

	// should contain the 2 items with summary
	if !strings.Contains(xmlStr, "Test Article") {
		t.Error("expected 'Test Article' in XML")
	}
	if !strings.Contains(xmlStr, "Article With Comments") {
		t.Error("expected 'Article With Comments' in XML")
	}

	// should NOT contain the item without summary
	if strings.Contains(xmlStr, "No Summary") {
		t.Error("expected item without summary to be omitted")
	}

	// should contain bold formatting (goldmark uses <strong>)
	if !strings.Contains(xmlStr, "<strong>summary</strong>") {
		t.Error("expected bold formatting in XML")
	}

	// should contain comments link
	if !strings.Contains(xmlStr, "Comments:") {
		t.Error("expected comments link in XML")
	}
}

// test `PublishXML` with XSS-like content
func TestPublishXMLEscaping(t *testing.T) {
	client := NewClient([]string{"key"}, nil)

	items := []CachedItem{
		{
			Title:       "XSS Test",
			Link:        "https://example.com",
			GUID:        `guid-with"quotes`,
			Description: "Test",
			Summary:     "Summary content",
		},
	}

	bytes, err := client.PublishXML("Feed", "https://example.com", "Desc", "Author", "e@e.com", items)
	if err != nil {
		t.Fatalf("PublishXML failed: %s", err)
	}

	xmlStr := string(bytes)

	// GUID with quotes should be escaped in href
	if strings.Contains(xmlStr, `href="guid-with"quotes"`) {
		t.Error("expected quotes in GUID to be escaped")
	}
}

// test `FetchFeeds` with httptest
func TestFetchFeeds(t *testing.T) {
	rssFeed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <item>
      <title>Article 1</title>
      <link>https://example.com/1</link>
      <guid>guid-fetch-1</guid>
      <pubDate>` + time.Now().Format(time.RFC1123Z) + `</pubDate>
    </item>
    <item>
      <title>Article 2</title>
      <link>https://example.com/2</link>
      <guid>guid-fetch-2</guid>
      <pubDate>` + time.Now().Format(time.RFC1123Z) + `</pubDate>
    </item>
  </channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, rssFeed)
	}))
	defer server.Close()

	client := NewClient([]string{"key"}, []string{server.URL})

	ctx := context.Background()
	feeds, err := client.FetchFeeds(ctx, false, 7)
	if err != nil {
		t.Fatalf("FetchFeeds failed: %s", err)
	}
	if len(feeds) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(feeds))
	}
	if len(feeds[0].Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(feeds[0].Items))
	}
}

// test `FetchFeeds` with ignoreAlreadyCached
func TestFetchFeedsIgnoreCached(t *testing.T) {
	rssFeed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <item>
      <title>Cached Article</title>
      <link>https://example.com/cached</link>
      <guid>guid-cached</guid>
      <pubDate>` + time.Now().Format(time.RFC1123Z) + `</pubDate>
    </item>
    <item>
      <title>New Article</title>
      <link>https://example.com/new</link>
      <guid>guid-new</guid>
      <pubDate>` + time.Now().Format(time.RFC1123Z) + `</pubDate>
    </item>
  </channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, rssFeed)
	}))
	defer server.Close()

	client := NewClient([]string{"key"}, []string{server.URL})

	// pre-cache one item
	_ = client.cache.Save(testFeedItem("guid-cached", "Cached Article"), "Cached", "Summary")

	ctx := context.Background()
	feeds, err := client.FetchFeeds(ctx, true, 7)
	if err != nil {
		t.Fatalf("FetchFeeds failed: %s", err)
	}
	if len(feeds) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(feeds))
	}
	if len(feeds[0].Items) != 1 {
		t.Errorf("expected 1 item (cached one filtered), got %d", len(feeds[0].Items))
	}
	if feeds[0].Items[0].GUID != "guid-new" {
		t.Errorf("expected 'guid-new', got %q", feeds[0].Items[0].GUID)
	}
}

// test `FetchFeeds` with HTTP error
func TestFetchFeedsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient([]string{"key"}, []string{server.URL})

	ctx := context.Background()
	feeds, err := client.FetchFeeds(ctx, false, 7)
	if err == nil {
		t.Error("expected error for HTTP 500")
	}
	if len(feeds) != 0 {
		t.Errorf("expected 0 feeds, got %d", len(feeds))
	}
}

// test `FetchFeeds` with old items filtered
func TestFetchFeedsOldItemsFiltered(t *testing.T) {
	oldDate := time.Now().Add(-30 * 24 * time.Hour) // 30 days ago
	rssFeed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <item>
      <title>Old Article</title>
      <link>https://example.com/old</link>
      <guid>guid-old</guid>
      <pubDate>` + oldDate.Format(time.RFC1123Z) + `</pubDate>
    </item>
    <item>
      <title>Recent Article</title>
      <link>https://example.com/recent</link>
      <guid>guid-recent</guid>
      <pubDate>` + time.Now().Format(time.RFC1123Z) + `</pubDate>
    </item>
  </channel>
</rss>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, rssFeed)
	}))
	defer server.Close()

	client := NewClient([]string{"key"}, []string{server.URL})

	ctx := context.Background()
	feeds, err := client.FetchFeeds(ctx, false, 7) // ignore items older than 7 days
	if err != nil {
		t.Fatalf("FetchFeeds failed: %s", err)
	}
	if len(feeds) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(feeds))
	}
	if len(feeds[0].Items) != 1 {
		t.Errorf("expected 1 item (old one filtered), got %d", len(feeds[0].Items))
	}
	if feeds[0].Items[0].GUID != "guid-recent" {
		t.Errorf("expected 'guid-recent', got %q", feeds[0].Items[0].GUID)
	}
}
