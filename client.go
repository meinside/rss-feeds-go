package rf

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/feeds"
	gf "github.com/gorilla/feeds"

	ssg "github.com/meinside/simple-scrapper-go"
)

const (
	fetchFeedsTimeoutSeconds = 10 // 10 seconds's timeout for fetching feeds
	summarizeTimeoutSeconds  = 60 // 60 seconds' timeout for generations

	defaultSummarizeIntervalSeconds = 10 // 10 seconds' interval between generations
	defaultDesiredLanguage          = "English"
)

// Client struct
type Client struct {
	feedsURLs []string
	cache     FeedsItemsCache

	googleAIAPIKey string
	googleAIModel  string

	desiredLanguage          string
	summarizeIntervalSeconds int
	verbose                  bool
}

// NewClient returns a new client with memory cache.
func NewClient(googleAIAPIKey string, feedsURLs []string) *Client {
	return &Client{
		feedsURLs: feedsURLs,
		cache:     newMemCache(),

		googleAIAPIKey: googleAIAPIKey,
		googleAIModel:  defaultGoogleAIModel,

		desiredLanguage: defaultDesiredLanguage,
	}
}

// NewClientWithDB returns a new client with SQLite DB cache.
func NewClientWithDB(googleAIAPIKey string, feedsURLs []string, dbFilepath string) (client *Client, err error) {
	if dbCache, err := newDBCache(dbFilepath); err == nil {
		return &Client{
			feedsURLs: feedsURLs,
			cache:     dbCache,

			googleAIAPIKey: googleAIAPIKey,
			googleAIModel:  defaultGoogleAIModel,

			desiredLanguage:          defaultDesiredLanguage,
			summarizeIntervalSeconds: defaultSummarizeIntervalSeconds,
		}, nil
	} else {
		return nil, fmt.Errorf("Failed to create a client with DB: %s", err)
	}
}

// SetGoogleAIModel sets the client's Google AI model.
func (c *Client) SetGoogleAIModel(model string) {
	c.googleAIModel = model
}

// SetDesiredLanguage sets the client's desired language for summaries.
func (c *Client) SetDesiredLanguage(lang string) {
	c.desiredLanguage = lang
}

// SetSummarizeIntervalSeconds sets the client's summarize interval seconds.
func (c *Client) SetSummarizeIntervalSeconds(seconds int) {
	c.summarizeIntervalSeconds = seconds
}

// SetVerbose sets the client's verbose mode.
func (c *Client) SetVerbose(v bool) {
	c.verbose = v
	c.cache.SetVerbose(v)
}

// FetchFeeds fetches feeds.
func (c *Client) FetchFeeds(ignoreAlreadyCached bool) (feeds []gf.RssFeed, err error) {
	feeds = []gf.RssFeed{}
	errs := []error{}

	client := &http.Client{
		Timeout: time.Duration(fetchFeedsTimeoutSeconds) * time.Second,
	}

	var fetched gf.RssFeedXml
	for _, url := range c.feedsURLs {
		if c.verbose {
			log.Printf("[verbose] fetching rss feeds from url: %s", url)
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %s", err)
		}
		req.Header.Set("User-Agent", fakeUserAgent)
		req.Header.Set("Content-Type", "text/xml;charset=UTF-8")

		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()

			if resp.StatusCode == 200 {
				contentType := resp.Header.Get("Content-Type")

				if strings.HasPrefix(contentType, "text/xml") ||
					strings.HasPrefix(contentType, "application/xml") ||
					strings.HasPrefix(contentType, "application/rss+xml") {
					if bytes, err := io.ReadAll(resp.Body); err == nil {
						if err := xml.Unmarshal(bytes, &fetched); err == nil {
							if c.verbose {
								log.Printf("[verbose] fetched %d item(s)", len(fetched.Channel.Items))
							}

							if ignoreAlreadyCached {
								// delete if it already exists in the cache
								fetched.Channel.Items = slices.DeleteFunc(fetched.Channel.Items, func(item *gf.RssItem) bool {
									exists := c.cache.Exists(item.Guid.Id)
									if c.verbose && exists {
										log.Printf("[verbose] ignoring: '%s' (%s)", item.Title, item.Guid.Id)
									}
									return exists
								})
							}

							if c.verbose {
								log.Printf("[verbose] returning %d item(s)", len(fetched.Channel.Items))
							}

							feeds = append(feeds, *fetched.Channel)
						} else {
							errs = append(errs, fmt.Errorf("failed to parse rss feeds from %s: %s", url, err))
						}
					} else {
						errs = append(errs, fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err))
					}
				} else {
					errs = append(errs, fmt.Errorf("content type '%s' not supported for url: %s", contentType, url))
				}
			} else {
				errs = append(errs, fmt.Errorf("http error %d from url: %s", resp.StatusCode, url))
			}
		} else {
			errs = append(errs, fmt.Errorf("failed to fetch rss feeds from url: %s", err))
		}
	}

	if len(errs) > 0 {
		err = errors.Join(errs...)
	}

	return feeds, err
}

// SummarizeAndCacheFeeds summarizes given feeds items and caches them.
func (c *Client) SummarizeAndCacheFeeds(feeds []gf.RssFeed, urlScrapper ...*ssg.Scrapper) (err error) {
	errs := []error{}

	for _, f := range feeds {
		for i, item := range f.Items {
			// summarize,
			summarized, err := c.summarize(item.Link, urlScrapper...)
			if err != nil {
				errs = append(errs, err)
			}

			// cache, (or update)
			c.cache.Save(*item, summarized)

			// and sleep for a while
			if i < len(f.Items)-1 {
				time.Sleep(time.Duration(c.summarizeIntervalSeconds) * time.Second)
			}
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// summarize the content of given `url`
func (c *Client) summarize(url string, urlScrapper ...*ssg.Scrapper) (summarized string, err error) {
	ctx, cancel := context.WithTimeout(context.TODO(), summarizeTimeoutSeconds*time.Second)
	defer cancel()

	if c.verbose {
		log.Printf("[verbose] summarizing content of url: %s", url)
	}

	var text string

	if len(urlScrapper) > 0 {
		scrapper := urlScrapper[0]

		var crawled map[string]string
		crawled, err = scrapper.CrawlURLs([]string{url}, true)

		for _, v := range crawled {
			text = v // get the first (and the only one) value
			break
		}
	} else {
		text, err = urlToText(url, c.verbose)
	}

	if err == nil {
		prompt := fmt.Sprintf(summaryPromptFormat, c.desiredLanguage, text)

		if summarized, err = c.generate(ctx, prompt); err == nil {
			return summarized, nil
		} else {
			if c.verbose {
				log.Printf("[verbose] failed to generate summary with prompt: '%s', error: %s", prompt, errorString(err))
			}
		}
	}

	return fmt.Sprintf("Summary failed with error: %s", errorString(err)), err
}

// ListCachedItems lists cached items.
func (c *Client) ListCachedItems(includeItemsMarkedAsRead bool) []CachedItem {
	return redactItems(c.cache.List(includeItemsMarkedAsRead), []string{
		c.googleAIAPIKey,
	})
}

// MarkCachedItemsAsRead marks given cached items as read.
func (c *Client) MarkCachedItemsAsRead(items []CachedItem) {
	for _, item := range items {
		c.cache.MarkAsRead(item.GUID)
	}
}

// DeleteOldCachedItems deletes old cached items.
func (c *Client) DeleteOldCachedItems() {
	c.cache.DeleteOlderThan1Month()
}

// PublishXML returns XML bytes of given cached items.
func (c *Client) PublishXML(title, link, description, author, email string, items []CachedItem) (bytes []byte, err error) {
	feed := &feeds.Feed{
		Title:       title,
		Link:        &feeds.Link{Href: link},
		Description: description,
		Author:      &feeds.Author{Name: author, Email: email},
		Created:     time.Now(),
	}

	items = slices.DeleteFunc(items, func(item CachedItem) bool {
		return len(item.Summary) <= 0
	})

	var feedItems []*feeds.Item
	for _, item := range items {
		content := item.Summary
		if len(item.Comments) > 0 {
			content += `<br><br>` + fmt.Sprintf(`Comments: <a href="%[1]s">%[1]s</a>`, item.Comments)
		} else {
			content += `<br><br>` + fmt.Sprintf(`GUID: <a href="%[1]s">%[1]s</a>`, item.GUID)
		}

		feedItem := feeds.Item{
			Id:          item.GUID,
			Title:       item.Title,
			Link:        &feeds.Link{Href: item.Link},
			Description: item.Description,
			Content:     content,
			Created:     item.CreatedAt,
			Updated:     item.UpdatedAt,
		}

		feedItems = append(feedItems, &feedItem)
	}
	feed.Items = feedItems

	rssFeed := (&feeds.Rss{
		Feed: feed,
	}).RssFeed()

	return xml.MarshalIndent(rssFeed.FeedXml(), "", "  ")
}
