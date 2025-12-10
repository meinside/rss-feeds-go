// Package rf for handling RSS feeds
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

	"github.com/gorilla/feeds"
	"github.com/mmcdole/gofeed"

	gt "github.com/meinside/gemini-things-go"
	ssg "github.com/meinside/simple-scrapper-go"
)

const (
	summarizeTimeoutSeconds = 6 * 60 // timeout seconds for summary (should be enough for `get content type + fetch (retry) + generation`)

	defaultSummarizeIntervalSeconds = 10 // 10 seconds' interval between summaries
	defaultDesiredLanguage          = "English"

	maxRetryCount = 3
)

const (
	ErrorPrefixSummaryFailedWithError = `Summary failed with error`

	PublishContentType = `application/rss+xml`
)

// Client struct
type Client struct {
	feedsURLs []string
	cache     FeedsItemsCache

	googleAIAPIKeys []string
	googleAIModel   string

	desiredLanguage          string
	summarizeIntervalSeconds int
	verbose                  bool

	_requestCountForAPIKeyRotation int
}

// NewClient returns a new client with memory cache.
func NewClient(
	googleAIAPIKeys []string,
	feedsURLs []string,
) *Client {
	return &Client{
		feedsURLs: feedsURLs,
		cache:     newMemCache(),

		googleAIAPIKeys: googleAIAPIKeys,
		googleAIModel:   defaultGoogleAIModel,

		desiredLanguage:          defaultDesiredLanguage,
		summarizeIntervalSeconds: defaultSummarizeIntervalSeconds,
	}
}

// NewClientWithDB returns a new client with SQLite DB cache.
func NewClientWithDB(
	googleAIAPIKeys []string,
	feedsURLs []string,
	dbFilepath string,
) (client *Client, err error) {
	if dbCache, err := newDBCache(dbFilepath); err == nil {
		return &Client{
			feedsURLs: feedsURLs,
			cache:     dbCache,

			googleAIAPIKeys: googleAIAPIKeys,
			googleAIModel:   defaultGoogleAIModel,

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
func (c *Client) FetchFeeds(
	ctx context.Context,
	ignoreAlreadyCached bool,
	ignoreItemsPublishedBeforeDays uint,
) (feeds []gofeed.Feed, err error) {
	feeds = []gofeed.Feed{}
	errs := []error{}

	client := http.DefaultClient

	for _, url := range c.feedsURLs {
		v(c.verbose, "fetching feeds from url: %s", url)

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("User-Agent", fakeUserAgent)
		req.Header.Set("Content-Type", "text/xml;charset=UTF-8")

		if resp, err := client.Do(req); err == nil {
			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode == 200 {
				contentType := resp.Header.Get("Content-Type")

				if bytes, err := io.ReadAll(resp.Body); err == nil {
					fp := gofeed.NewParser()
					if fetched, err := fp.ParseString(string(bytes)); err == nil {
						v(c.verbose, "fetched %d item(s)", len(fetched.Items))

						if ignoreAlreadyCached {
							// delete if it already exists in the cache
							fetched.Items = slices.DeleteFunc(fetched.Items, func(item *gofeed.Item) bool {
								exists := c.cache.Exists(item.GUID)
								if exists {
									v(c.verbose, "ignoring already cached item: '%s' (%s)", item.Title, item.GUID)
								}
								return exists
							})
						}

						// delete if it was published too long ago
						fetched.Items = slices.DeleteFunc(fetched.Items, func(item *gofeed.Item) bool {
							before := item.PublishedParsed.Before(time.Now().Add(time.Duration(-ignoreItemsPublishedBeforeDays) * 24 * time.Hour))
							if before {
								v(c.verbose, "ignoring item older than %d days: '%s' (%s)", ignoreItemsPublishedBeforeDays, item.Title, item.GUID)
							}
							return before
						})

						v(c.verbose, "returning %d item(s)", len(fetched.Items))

						feeds = append(feeds, *fetched)
					} else {
						errs = append(errs, fmt.Errorf("failed to parse feeds from '%s': %w", url, err))
					}
				} else {
					errs = append(errs, fmt.Errorf("failed to read '%s' document from '%s': %w", contentType, url, err))
				}
			} else {
				errs = append(errs, fmt.Errorf("http error %d from url: '%s'", resp.StatusCode, url))
			}
		} else {
			errs = append(errs, fmt.Errorf("failed to fetch feeds from url: %w", err))
		}
	}

	if len(errs) > 0 {
		err = errors.Join(errs...)
	}

	return feeds, err
}

// SummarizeAndCacheFeeds summarizes given feeds items and caches them.
//
// Each feed item will be summarized with a timeout of `summarizeTimeoutSeconds` seconds.
//
// If summary fails, the original content prepended with the error message will be cached.
//
// If there was a retriable error(eg. model overloads), it will return immediately.
// (remaining feed items will be retried later)
func (c *Client) SummarizeAndCacheFeeds(
	feeds []gofeed.Feed,
	urlScrapper ...*ssg.Scrapper,
) (err error) {
	errs := []error{}

outer:
	for _, f := range feeds {
		for i, item := range f.Items {
			// context with timeout
			ctx, cancel := context.WithTimeout(
				context.TODO(),
				summarizeTimeoutSeconds*time.Second,
			)
			defer cancel()

			// summarize,
			translatedTitle, summarizedContent, err := c.summarize(
				ctx,
				item.Title,
				item.Link,
				urlScrapper...,
			)
			if err != nil {
				// NOTE: skip remaining feed items if err is:
				//   - http 503 ('The model is overloaded. Please try again later.')
				// for retyring later
				if gt.IsModelOverloaded(err) {
					v(c.verbose, "skipping remaining feed items due to overloaded model (will be retried later)")

					errs = append(errs, err)

					break outer
				}

				// prepend error text to the original content
				summarizedContent = fmt.Sprintf("%s\n\n%s", summarizedContent, item.Description)

				errs = append(errs, fmt.Errorf("failed to summarize item '%s' (%s): %w", item.Title, item.Link, err))
			} else {
				// append the result of summary to the content
				summarizedContent = fmt.Sprintf(
					"%s\n\n(summarized with **%s**, %s)",
					summarizedContent,
					c.googleAIModel,
					time.Now().Format("2006-01-02 15:04:05 (Mon) MST"),
				)
			}

			// cache, (or update)
			c.cache.Save(*item, translatedTitle, summarizedContent)

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
func (c *Client) summarize(
	ctx context.Context,
	title, url string,
	urlScrapper ...*ssg.Scrapper,
) (translatedTitle, summarizedContent string, err error) {
	if isYouTubeURL(url) {
		url = normalizeYouTubeURL(url)

		v(c.verbose, "summarizing youtube url: %s", url)

		// context with timeout
		ctxGenerate, cancelGenerate := context.WithTimeout(ctx, generationTimeoutSeconds*time.Second)
		defer cancelGenerate()

		// summarize & translate given title and youtube url
		if translatedTitle, summarizedContent, err = c.translateAndSummarizeYouTube(ctxGenerate, title, url); err == nil {
			return translatedTitle, summarizedContent, err
		} else {
			v(c.verbose, "failed to generate summary from youtube url: '%s', error: %s", url, gt.ErrToStr(err))
		}
	} else {
		v(c.verbose, "summarizing content of url: %s", url)

		// context with timeout
		ctxGenerate, cancelGenerate := context.WithTimeout(ctx, generationTimeoutSecondsForYoutube*time.Second)
		defer cancelGenerate()

		// fetch the content of given url and summarize & translate it
		var fetched []byte
		var contentType string
		if fetched, contentType, err = c.fetch(maxRetryCount, url, urlScrapper...); err == nil {
			if isTextFormattableContent(contentType) { // use text prompt
				prompt := fmt.Sprintf(summarizeContentPromptFormat, c.desiredLanguage, title, string(fetched))

				if translatedTitle, summarizedContent, err = c.translateAndSummarize(ctxGenerate, prompt); err == nil {
					// FIXME: sometimes translated/summarized results are empty
					if len(translatedTitle) <= 0 {
						translatedTitle = title
					}
					if len(summarizedContent) <= 0 {
						summarizedContent = summarizedContentEmpty
					}

					return translatedTitle, summarizedContent, err
				} else {
					v(c.verbose, "failed to generate summary with prompt: '%s', error: %s", prompt, gt.ErrToStr(err))
				}
			} else if isFileContent(contentType) { // use prompt with files
				prompt := fmt.Sprintf(summarizeContentFilePromptFormat, c.desiredLanguage, title)

				if translatedTitle, summarizedContent, err = c.translateAndSummarize(ctxGenerate, prompt, fetched); err == nil {
					// FIXME: sometimes translated/summarized results are empty
					if len(translatedTitle) <= 0 {
						translatedTitle = title
					}
					if len(summarizedContent) <= 0 {
						summarizedContent = summarizedContentEmpty
					}

					return translatedTitle, summarizedContent, err
				} else {
					v(c.verbose, "failed to generate summary with prompt and file: '%s', error: %s", prompt, gt.ErrToStr(err))
				}
			} else {
				err = fmt.Errorf("not a summarizable content type: %s", contentType)
			}
		} else {
			if translatedTitle, summarizedContent, err = c.summarizeURL(ctxGenerate, title, url, c.desiredLanguage); err == nil {
				// FIXME: sometimes summarized results are empty
				if len(summarizedContent) <= 0 {
					summarizedContent = summarizedContentEmpty
				}

				return translatedTitle, summarizedContent, err
			} else {
				v(c.verbose, "failed to generate summary with url: '%s', error: %s", url, gt.ErrToStr(err))
			}
		}
	}

	// return error message
	return title, fmt.Sprintf("%s: %s", ErrorPrefixSummaryFailedWithError, gt.ErrToStr(err)), err
}

// fetch url content with or without url scrapper
func (c *Client) fetch(
	remainingRetryCount int,
	url string,
	urlScrapper ...*ssg.Scrapper,
) (scrapped []byte, contentType string, err error) {
	contentType, _ = getContentType(url, c.verbose)

	if len(urlScrapper) > 0 && strings.HasPrefix(contentType, "text/html") { // if scrapper is given, and content-type is HTML, use it
		scrapper := urlScrapper[0]

		var crawled map[string]string
		crawled, err = scrapper.CrawlURLs([]string{url}, true)

		for _, v := range crawled {
			// get the first (and the only one) value
			scrapped = fmt.Appendf(nil, urlToTextFormat, url, contentType, v)
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

// return a rotated api key
func (c *Client) rotatedAPIKey() string {
	rotated := c.googleAIAPIKeys[c._requestCountForAPIKeyRotation%len(c.googleAIAPIKeys)]
	c._requestCountForAPIKeyRotation++

	return rotated
}

// ListCachedItems lists cached items.
func (c *Client) ListCachedItems(includeItemsMarkedAsRead bool) []CachedItem {
	return redactItems(c.cache.List(includeItemsMarkedAsRead), c.googleAIAPIKeys)
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

// PublishXML returns XML bytes (application/rss+xml) of given cached items.
func (c *Client) PublishXML(
	title, link, description, author, email string,
	items []CachedItem,
) (bytes []byte, err error) {
	feed := &feeds.Feed{
		Title:       title,
		Link:        &feeds.Link{Href: link},
		Description: description,
		Author:      &feeds.Author{Name: author, Email: email},
		Created:     time.Now(),
	}

	// NOTE: drop items without summary (omit feed items that are not summarized yet)
	items = slices.DeleteFunc(items, func(item CachedItem) bool {
		return len(item.Summary) <= 0
	})

	var feedItems []*feeds.Item
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

		feedItem := feeds.Item{
			Id:    item.GUID,
			Title: item.Title,
			Link: &feeds.Link{
				Href: item.Link,
			},
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
