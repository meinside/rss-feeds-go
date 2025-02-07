package rf

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	gf "github.com/gorilla/feeds"
	"google.golang.org/api/googleapi"

	ssg "github.com/meinside/simple-scrapper-go"
)

const (
	fetchFeedsTimeoutSeconds = 30     // 30 seconds's timeout for fetching feeds
	summarizeTimeoutSeconds  = 3 * 60 // 3 minutes' timeout for `get content type + fetch (retry) + generation`

	defaultSummarizeIntervalSeconds = 10 // 10 seconds' interval between summaries
	defaultDesiredLanguage          = "English"

	maxRetryCount = 3
)

const (
	ErrorPrefixSummaryFailedWithError = `Summary failed with error`
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

		desiredLanguage:          defaultDesiredLanguage,
		summarizeIntervalSeconds: defaultSummarizeIntervalSeconds,
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
		return nil, fmt.Errorf("failed to create a client with DB: %w", err)
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
		v(c.verbose, "fetching rss feeds from url: %s", url)

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
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
							v(c.verbose, "fetched %d item(s)", len(fetched.Channel.Items))

							if ignoreAlreadyCached {
								// delete if it already exists in the cache
								fetched.Channel.Items = slices.DeleteFunc(fetched.Channel.Items, func(item *gf.RssItem) bool {
									exists := c.cache.Exists(item.Guid.Id)
									if exists {
										v(c.verbose, "ignoring: '%s' (%s)", item.Title, item.Guid.Id)
									}
									return exists
								})
							}

							v(c.verbose, "returning %d item(s)", len(fetched.Channel.Items))

							feeds = append(feeds, *fetched.Channel)
						} else {
							errs = append(errs, fmt.Errorf("failed to parse rss feeds from '%s': %w", url, err))
						}
					} else {
						errs = append(errs, fmt.Errorf("failed to read '%s' document from '%s': %w", contentType, url, err))
					}
				} else {
					errs = append(errs, fmt.Errorf("content type '%s' not supported for url: '%s'", contentType, url))
				}
			} else {
				errs = append(errs, fmt.Errorf("http error %d from url: '%s'", resp.StatusCode, url))
			}
		} else {
			errs = append(errs, fmt.Errorf("failed to fetch rss feeds from url: %w", err))
		}
	}

	if len(errs) > 0 {
		err = errors.Join(errs...)
	}

	return feeds, err
}

// SummarizeAndCacheFeeds summarizes given feeds items and caches them.
//
// If summary fails, the original content prepended with the error message will be cached.
//
// If there was an error with quota (HTTP 429), it will return immediately.
// (remaining feed items can be retried later)
func (c *Client) SummarizeAndCacheFeeds(feeds []gf.RssFeed, urlScrapper ...*ssg.Scrapper) (err error) {
	errs := []error{}

outer:
	for _, f := range feeds {
		for i, item := range f.Items {
			// summarize,
			summarized, err := c.summarize(item.Link, urlScrapper...)
			if err != nil {
				// NOTE: skip remaining feed items if err is http 429 ('RESOURCE_EXHAUSTED')
				var gerr *googleapi.Error
				if errors.As(err, &gerr) && gerr.Code == 429 {
					v(c.verbose, "skipping remaining feed items due to quota limit")

					errs = append(errs, err)

					break outer
				}

				// prepend error text to the original content
				summarized = fmt.Sprintf("%s\n\n%s", summarized, item.Description)

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

	v(c.verbose, "summarizing content of url: %s", url)

	var fetched []byte
	var contentType string
	if fetched, contentType, err = c.fetch(maxRetryCount, url, urlScrapper...); err == nil {
		if isTextFormattableContent(contentType) { // use text prompt
			prompt := fmt.Sprintf(summarizeURLPromptFormat, c.desiredLanguage, string(fetched))

			if summarized, err = c.generate(ctx, prompt); err == nil {
				return summarized, nil
			} else {
				v(c.verbose, "failed to generate summary with prompt: '%s', error: %s", prompt, errorString(err))
			}
		} else if isFileContent(contentType) { // use prompt with files
			prompt := fmt.Sprintf(summarizeFilePromptFormat, c.desiredLanguage)

			if summarized, err = c.generate(ctx, prompt, fetched); err == nil {
				return summarized, nil
			} else {
				v(c.verbose, "failed to generate summary with prompt and file: '%s', error: %s", prompt, errorString(err))
			}
		} else {
			err = fmt.Errorf("not a summarizable content type: %s", contentType)
		}
	}

	// return error message
	return fmt.Sprintf("%s: %s", ErrorPrefixSummaryFailedWithError, errorString(err)), err
}

// fetch url content with or without url scrapper
func (c *Client) fetch(remainingRetryCount int, url string, urlScrapper ...*ssg.Scrapper) (scrapped []byte, contentType string, err error) {
	contentType, _ = getContentType(url, c.verbose)

	if len(urlScrapper) > 0 && strings.HasPrefix(contentType, "text/html") { // if scrapper is given, and content-type is HTML, use it
		scrapper := urlScrapper[0]

		var crawled map[string]string
		crawled, err = scrapper.CrawlURLs([]string{url}, true)

		for _, v := range crawled {
			// get the first (and the only one) value
			scrapped = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, v))
			break
		}
	} else { // otherwise, use `fetchURLContent` function
		scrapped, contentType, err = fetchURLContent(url, c.verbose)
	}

	// retry if needed
	if err != nil && remainingRetryCount > 0 {
		v(c.verbose, "retrying fetching from url '%s' (remaining count: %d)", url, remainingRetryCount)

		return c.fetch(remainingRetryCount-1, url, urlScrapper...)
	}

	// if all retries failed with urlScrapper, try without it
	if err != nil && remainingRetryCount == 0 && len(urlScrapper) > 0 {
		v(c.verbose, "fetching from url '%s' without url scrapper as a last try", url)

		scrapped, contentType, err = fetchURLContent(url, c.verbose)
	}

	return scrapped, contentType, err
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
	feed := &gf.Feed{
		Title:       title,
		Link:        &gf.Link{Href: link},
		Description: description,
		Author:      &gf.Author{Name: author, Email: email},
		Created:     time.Now(),
	}

	// drop items without summary
	items = slices.DeleteFunc(items, func(item CachedItem) bool {
		return len(item.Summary) <= 0
	})

	var feedItems []*gf.Item
	for _, item := range items {
		content := decorateHTML(item.Summary)

		// NOTE: if the summary was not successful, it is a concatenated string of the error message and original content
		if !isError(item.Summary) {
			// if it was a successful summary, append comments or GUID of the original content
			if len(item.Comments) > 0 {
				content += `<br><br>` + fmt.Sprintf(`Comments: <a href="%[1]s">%[1]s</a>`, item.Comments)
			} else {
				content += `<br><br>` + fmt.Sprintf(`GUID: <a href="%[1]s">%[1]s</a>`, item.GUID)
			}
		}

		feedItem := gf.Item{
			Id:          item.GUID,
			Title:       item.Title,
			Link:        &gf.Link{Href: item.Link},
			Description: item.Description,
			Content:     content,
			Created:     item.CreatedAt,
			Updated:     item.UpdatedAt,
		}

		feedItems = append(feedItems, &feedItem)
	}
	feed.Items = feedItems

	rssFeed := (&gf.Rss{
		Feed: feed,
	}).RssFeed()

	return xml.MarshalIndent(rssFeed.FeedXml(), "", "  ")
}
