// Package rf for handling RSS feeds
package rf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"math/rand"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

	defaultCooldownSeconds      = 60      // fallback cooldown when retryDelay is missing
	maxEscalatedCooldownSeconds = 30 * 60 // upper bound of the self-escalated cooldown (server-provided delays are honored as-is)
	maxCooldownFailures         = 10      // failure count at which the escalation stops growing
	cooldownJitterRatio         = 0.2     // up to +20% of jitter, to keep multiple instances from waking up together
	maxCooldownJitterSeconds    = 5 * 60  // cap of the jitter above, so that long cooldowns don't grow by hours

	// timezone in which the daily (RPD) quotas reset at midnight
	// (https://ai.google.dev/gemini-api/docs/rate-limits: "Requests per day (RPD)
	// quotas reset at midnight Pacific time.")
	dailyQuotaResetTimezone = "America/Los_Angeles"

	// substring which marks a `quotaId` of a daily quota
	// (eg. 'GenerateRequestsPerDayPerProjectPerModel-FreeTier')
	dailyQuotaIDMarker = "PerDay"

	// persisted cooldowns which expired longer than this ago are ignored on
	// load: the quota has long recovered, and their failure count must not
	// keep escalating forever
	staleCooldownSeconds = 60 * 60
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
	googleAIModels  []string

	desiredLanguage          string
	summarizeIntervalSeconds int
	verbose                  bool

	combos        []keyModelCombo
	cooldownUntil map[int]time.Time
	// cooldownFailures counts consecutive quota errors per combo index, for
	// escalating the cooldown of a combo that keeps failing.
	cooldownFailures map[int]int
	cooldownMu       sync.Mutex

	_numRequests atomic.Int64
}

// keyModelCombo is a single (api key, model) pairing subject to quota.
type keyModelCombo struct {
	apiKey string
	model  string

	// apiKeyHash identifies `apiKey` in persisted cooldowns, so that the key
	// itself is never written to the cache
	apiKeyHash string
}

// hashAPIKey returns a short, non-reversible id of given api key.
func hashAPIKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:8])
}

// NewClient returns a new client with memory cache.
func NewClient(
	googleAIAPIKeys []string,
	feedsURLs []string,
) *Client {
	c := &Client{
		feedsURLs: feedsURLs,
		cache:     newMemCache(),

		googleAIAPIKeys: googleAIAPIKeys,
		googleAIModels:  []string{defaultGoogleAIModel},

		desiredLanguage:          defaultDesiredLanguage,
		summarizeIntervalSeconds: defaultSummarizeIntervalSeconds,
	}
	c.buildCombos()
	return c
}

// NewClientWithDB returns a new client with SQLite DB cache.
func NewClientWithDB(
	googleAIAPIKeys []string,
	feedsURLs []string,
	dbFilepath string,
) (client *Client, err error) {
	if dbCache, err := newDBCache(dbFilepath); err == nil {
		c := &Client{
			feedsURLs: feedsURLs,
			cache:     dbCache,

			googleAIAPIKeys: googleAIAPIKeys,
			googleAIModels:  []string{defaultGoogleAIModel},

			desiredLanguage:          defaultDesiredLanguage,
			summarizeIntervalSeconds: defaultSummarizeIntervalSeconds,
		}
		c.buildCombos()
		return c, nil
	} else {
		return nil, fmt.Errorf("failed to create a client with DB: %w", err)
	}
}

// SetGoogleAIModels sets the client's Google AI models.
func (c *Client) SetGoogleAIModels(models []string) {
	c.googleAIModels = models
	c.buildCombos()
}

// buildCombos rebuilds the (key, model) combination list and restores the
// cooldowns persisted in the cache (dropping the ones which no longer match a
// combination, or which expired long ago).
func (c *Client) buildCombos() {
	c.cooldownMu.Lock()
	defer c.cooldownMu.Unlock()

	combos := make([]keyModelCombo, 0, len(c.googleAIAPIKeys)*len(c.googleAIModels))
	for _, key := range c.googleAIAPIKeys {
		hash := hashAPIKey(key)
		for _, model := range c.googleAIModels {
			combos = append(combos, keyModelCombo{apiKey: key, apiKeyHash: hash, model: model})
		}
	}
	c.combos = combos
	c.cooldownUntil = map[int]time.Time{}
	c.cooldownFailures = map[int]int{}

	c.restoreCooldownsLocked(time.Now())
}

// restoreCooldownsLocked restores the persisted cooldowns of the current
// combinations. The caller must hold `cooldownMu`.
func (c *Client) restoreCooldownsLocked(now time.Time) {
	if c.cache == nil {
		return
	}

	indices := make(map[[2]string]int, len(c.combos))
	for i, combo := range c.combos {
		indices[[2]string{combo.apiKeyHash, combo.model}] = i
	}

	stale := now.Add(-time.Duration(staleCooldownSeconds) * time.Second)
	for _, cooldown := range c.cache.LoadCooldowns() {
		idx, exists := indices[[2]string{cooldown.APIKeyHash, cooldown.Model}]
		if !exists || cooldown.Until.Before(stale) {
			continue
		}
		c.cooldownUntil[idx] = cooldown.Until
		c.cooldownFailures[idx] = cooldown.Failures

		v(c.verbose, "restored cooldown of model '%s' until: %s (failures: %d)", cooldown.Model, cooldown.Until, cooldown.Failures)
	}
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
) ([]gofeed.Feed, error) {
	var feeds []gofeed.Feed
	var errs []error

	for _, url := range c.feedsURLs {
		fetched, err := c.fetchSingleFeed(ctx, url, ignoreAlreadyCached, ignoreItemsPublishedBeforeDays)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		feeds = append(feeds, *fetched)
	}

	if len(errs) > 0 {
		return feeds, errors.Join(errs...)
	}

	return feeds, nil
}

// fetchSingleFeed fetches a single feed from the given URL with proper defer-based cleanup.
func (c *Client) fetchSingleFeed(
	ctx context.Context,
	url string,
	ignoreAlreadyCached bool,
	ignoreItemsPublishedBeforeDays uint,
) (*gofeed.Feed, error) {
	v(c.verbose, "fetching feeds from url: %s", url)

	client := &http.Client{
		Timeout: time.Duration(fetchURLTimeoutSeconds) * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", fakeUserAgent)
	req.Header.Set("Content-Type", "text/xml;charset=UTF-8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feeds from url: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http error %d from url: '%s'", resp.StatusCode, url)
	}

	contentType := resp.Header.Get("Content-Type")

	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read '%s' document from '%s': %w", contentType, url, err)
	}

	fp := gofeed.NewParser()
	fetched, err := fp.ParseString(string(bytes))
	if err != nil {
		return nil, fmt.Errorf("failed to parse feeds from '%s': %w", url, err)
	}

	v(c.verbose, "fetched %d item(s)", len(fetched.Items))

	if ignoreAlreadyCached {
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
		if item.PublishedParsed == nil {
			return false
		}
		before := item.PublishedParsed.Before(time.Now().Add(time.Duration(-ignoreItemsPublishedBeforeDays) * 24 * time.Hour))
		if before {
			v(c.verbose, "ignoring item older than %d days: '%s' (%s)", ignoreItemsPublishedBeforeDays, item.Title, item.GUID)
		}
		return before
	})

	v(c.verbose, "returning %d item(s)", len(fetched.Items))

	return fetched, nil
}

// SummarizeAndCacheFeeds summarizes given feeds items and caches them.
//
// Each feed item will be summarized with a timeout of `summarizeTimeoutSeconds` seconds.
//
// If summary fails, the original content prepended with the error message will be cached.
//
// If there was a retriable error(eg. model overloads or exhausted quota), it will
// return immediately. (remaining feed items will be retried later)
func (c *Client) SummarizeAndCacheFeeds(
	ctx context.Context,
	feeds []gofeed.Feed,
	urlScrapper ...*ssg.Scrapper,
) (err error) {
	var errs []error

outer:
	for _, f := range feeds {
		for i, item := range f.Items {
			// context with timeout
			itemCtx, cancel := context.WithTimeout(
				ctx,
				summarizeTimeoutSeconds*time.Second,
			)

			// summarize,
			usedModel, translatedTitle, summarizedContent, err := c.summarize(
				itemCtx,
				item.Title,
				item.Link,
				urlScrapper...,
			)
			cancel()

			if err != nil {
				// NOTE: skip remaining feed items if err is:
				//   - http 503 ('The model is overloaded. Please try again later.')
				//   - http 429 (quota exceeded), or every (key, model) combo is in cooldown
				// for retrying later
				//
				// NOTE: such items must not be cached, or their error messages would be
				// served as their summaries forever
				if isRetriableLater(err) {
					v(c.verbose, "skipping remaining feed items due to retriable error with model %s (will be retried later): %s", usedModel, gt.ErrToStr(err))

					errs = append(errs, err)

					break outer
				}

				// prepend error text to the original content
				summarizedContent = fmt.Sprintf("<p>%s</p>\n<hr>\n%s", summarizedContent, item.Description)

				errs = append(errs, fmt.Errorf("failed to summarize item '%s' (%s): %w", item.Title, item.Link, err))
			} else {
				// append the result of summary to the content
				summarizedContent = fmt.Sprintf(
					"%s\n\n(summarized with **%s**, %s)",
					summarizedContent,
					usedModel,
					time.Now().Format("2006-01-02 15:04:05 (Mon) MST"),
				)
			}

			// trim translated/summarized contents
			translatedTitle = strings.TrimSpace(translatedTitle)
			summarizedContent = strings.TrimSpace(summarizedContent)

			// cache, (or update)
			if cacheErr := c.cache.Save(*item, translatedTitle, summarizedContent); cacheErr != nil {
				errs = append(errs, fmt.Errorf("failed to cache item '%s': %w", item.Title, cacheErr))
			}

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

// isRetriableLater reports whether the error means the remaining feed items
// should be left untouched and retried in a later run, rather than cached with
// an error message as their summary.
func isRetriableLater(err error) bool {
	return gt.IsModelOverloaded(err) || // http 503 ('The model is overloaded.')
		isQuotaError(err) || // http 429 (quota exceeded)
		errors.Is(err, ErrNoAvailableAPIKey) // all (key, model) combos are in cooldown
}

// isQuotaError reports whether the error is an http 429 from the API.
//
// NOTE: `gt.IsQuotaExceeded` additionally requires the message to contain
// 'exceeded your current quota', so it misses 429s worded differently (eg.
// 'Resource has been exhausted'). Those must cool the combo down and fail over
// all the same, so the status code alone is enough here.
func isQuotaError(err error) bool {
	if gt.IsQuotaExceeded(err) {
		return true
	}
	if ae, isAPIError := gt.APIError(err); isAPIError {
		return ae.Code == http.StatusTooManyRequests
	}
	return false
}

// failedSummary builds the cached content for a failed summary, including the
// used model in the error prefix when known.
func failedSummary(usedModel string, err error) string {
	if usedModel != "" {
		return fmt.Sprintf("%s [%s]: %s", ErrorPrefixSummaryFailedWithError, usedModel, gt.ErrToStr(err))
	}
	return fmt.Sprintf("%s: %s", ErrorPrefixSummaryFailedWithError, gt.ErrToStr(err))
}

// summarize the content of given `url`
func (c *Client) summarize(
	ctx context.Context,
	title, url string,
	urlScrapper ...*ssg.Scrapper,
) (usedModel string, translatedTitle, summarizedContent string, err error) {
	if isYouTubeURL(url) {
		url = normalizeYouTubeURL(url)

		v(c.verbose, "summarizing youtube url: %s", url)

		usedModel, translatedTitle, summarizedContent, err = c.translateAndSummarizeYouTube(ctx, title, url)
		if err == nil {
			return usedModel, translatedTitle, summarizedContent, nil
		}

		v(c.verbose, "failed to generate summary from youtube url: '%s', error: %s", url, gt.ErrToStr(err))
		return usedModel, title, failedSummary(usedModel, err), err
	}

	v(c.verbose, "summarizing content of url: %s", url)

	// try fetching the content
	fetched, contentType, fetchErr := c.fetch(ctx, maxRetryCount, url, urlScrapper...)
	if fetchErr != nil {
		// fallback: summarize via Gemini URL context
		usedModel, translatedTitle, summarizedContent, err = c.summarizeURL(ctx, title, url, c.desiredLanguage)
		if err == nil {
			if len(summarizedContent) <= 0 {
				summarizedContent = summarizedContentEmpty
			}
			return usedModel, translatedTitle, summarizedContent, nil
		}

		v(c.verbose, "failed to generate summary with url: '%s', error: %s", url, gt.ErrToStr(err))
		return usedModel, title, failedSummary(usedModel, err), err
	}

	// summarize fetched content based on type
	switch {
	case isTextFormattableContent(contentType):
		prompt := fmt.Sprintf(summarizeContentPromptFormat, c.desiredLanguage, title, string(fetched))
		usedModel, translatedTitle, summarizedContent, err = c.translateAndSummarize(ctx, prompt)
	case isFileContent(contentType):
		prompt := fmt.Sprintf(summarizeContentFilePromptFormat, c.desiredLanguage, title)
		usedModel, translatedTitle, summarizedContent, err = c.translateAndSummarize(ctx, prompt, fetched)
	default:
		err = fmt.Errorf("not a summarizable content type: %s", contentType)
	}

	if err != nil {
		v(c.verbose, "failed to generate summary for '%s', error: %s", url, gt.ErrToStr(err))
		return usedModel, title, failedSummary(usedModel, err), err
	}

	if len(translatedTitle) <= 0 {
		translatedTitle = title
	}
	if len(summarizedContent) <= 0 {
		summarizedContent = summarizedContentEmpty
	}

	return usedModel, translatedTitle, summarizedContent, nil
}

// fetch url content with or without url scrapper
func (c *Client) fetch(
	ctx context.Context,
	remainingRetryCount int,
	url string,
	urlScrapper ...*ssg.Scrapper,
) (scrapped []byte, contentType string, err error) {
	contentType, _ = getContentType(ctx, url, c.verbose)

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
		scrapped, contentType, err = fetchURLContent(ctx, url, c.verbose)
	}

	// retry if needed
	if err != nil && remainingRetryCount > 0 {
		v(c.verbose, "retrying fetching from url '%s' (remaining count: %d)", url, remainingRetryCount)

		return c.fetch(ctx, remainingRetryCount-1, url, urlScrapper...)
	}

	// if all retries failed with urlScrapper, try without it
	if err != nil && remainingRetryCount == 0 && len(urlScrapper) > 0 {
		v(c.verbose, "fetching from url '%s' without url scrapper as a last try", url)

		scrapped, contentType, err = fetchURLContent(ctx, url, c.verbose)
	}

	return scrapped, contentType, err
}

// ErrNoAvailableAPIKey is returned when every (key, model) combo is in cooldown.
var ErrNoAvailableAPIKey = errors.New("no available api key/model (all in cooldown)")

// cooldownDuration derives a cooldown duration from a quota (429) error.
//
// A server-provided RetryInfo.retryDelay is honored as-is on the first failure.
// From the second consecutive failure of the same combo on, the duration is
// raised to at least `defaultCooldownSeconds * 2^(failures-1)` (capped at
// `maxEscalatedCooldownSeconds`), so that a combo whose quota is exhausted for
// far longer than it admits is not hammered once a minute.
//
// When the error names a daily (RPD) quota, the cooldown lasts until that quota
// resets instead: such a 429 is often served with a retryDelay of half a minute
// even though nothing will succeed until midnight Pacific time.
//
// Jitter of up to `cooldownJitterRatio` (at most `maxCooldownJitterSeconds`) is
// added on top.
//
// `failures` is the number of consecutive quota errors of the combo, starting at 1.
func cooldownDuration(err error, failures int, now time.Time) time.Duration {
	fallback := time.Duration(defaultCooldownSeconds) * time.Second
	details := gt.ErrDetails(err)

	delay, ok := parseRetryDelay(details)
	if !ok {
		delay = fallback
	}

	// a daily quota does not recover before it resets
	if isDailyQuotaExceeded(details) {
		if reset, resolved := nextDailyQuotaReset(now); resolved {
			delay = max(delay, reset.Sub(now))
		}
	}

	// escalate: the floor doubles with each consecutive failure
	if failures > 1 {
		shift := min(failures, maxCooldownFailures) - 1
		floor := min(fallback<<shift, time.Duration(maxEscalatedCooldownSeconds)*time.Second)
		delay = max(delay, floor)
	}

	// and jitter
	jitter := min(
		time.Duration(rand.Float64()*cooldownJitterRatio*float64(delay)),
		time.Duration(maxCooldownJitterSeconds)*time.Second,
	)
	return delay + jitter
}

// isDailyQuotaExceeded reports whether the error details name a daily (RPD)
// quota, ie. a google.rpc.QuotaFailure violation whose `quotaId` contains
// `dailyQuotaIDMarker`.
func isDailyQuotaExceeded(details []map[string]any) bool {
	for _, d := range details {
		if t, _ := d["@type"].(string); !strings.Contains(t, "QuotaFailure") {
			continue
		}
		violations, isSlice := d["violations"].([]any)
		if !isSlice {
			continue
		}
		for _, v := range violations {
			violation, isMap := v.(map[string]any)
			if !isMap {
				continue
			}
			if quotaID, _ := violation["quotaId"].(string); strings.Contains(quotaID, dailyQuotaIDMarker) {
				return true
			}
		}
	}
	return false
}

// nextDailyQuotaReset returns the first midnight in `dailyQuotaResetTimezone`
// after `now`. resolved is false when the timezone is unavailable (eg. a system
// without tzdata), in which case the caller keeps its own duration.
func nextDailyQuotaReset(now time.Time) (reset time.Time, resolved bool) {
	loc, err := time.LoadLocation(dailyQuotaResetTimezone)
	if err != nil {
		log.Printf("failed to load timezone '%s' for the daily quota reset: %s", dailyQuotaResetTimezone, err)
		return time.Time{}, false
	}

	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).
		AddDate(0, 0, 1), true
}

// parseRetryDelay extracts google.rpc.RetryInfo.retryDelay (e.g. "37s") from
// error details. ok is false if it is absent or unparsable.
func parseRetryDelay(details []map[string]any) (delay time.Duration, ok bool) {
	for _, d := range details {
		t, _ := d["@type"].(string)
		if !strings.Contains(t, "RetryInfo") {
			continue
		}
		str, isString := d["retryDelay"].(string)
		if !isString {
			return 0, false
		}
		if parsed, perr := time.ParseDuration(str); perr == nil && parsed > 0 {
			return parsed, true
		}
		return 0, false
	}
	return 0, false
}

// pickAvailableCombo returns the next combo (round-robin from the global
// counter) whose cooldown has expired at `now` and whose model is not in
// `skipModels`. ok is false if no such combo is left.
func (c *Client) pickAvailableCombo(now time.Time, skipModels map[string]bool) (combo keyModelCombo, idx int, ok bool) {
	start := int(c._numRequests.Add(1) - 1)

	c.cooldownMu.Lock()
	defer c.cooldownMu.Unlock()

	n := len(c.combos)
	for i := 0; i < n; i++ {
		candidate := (start + i) % n
		if skipModels[c.combos[candidate].model] {
			continue
		}
		until, inCooldown := c.cooldownUntil[candidate]
		if inCooldown && until.After(now) {
			continue
		}
		return c.combos[candidate], candidate, true
	}
	return keyModelCombo{}, 0, false
}

// ListCachedItems lists cached items.
func (c *Client) ListCachedItems(includeItemsMarkedAsRead bool) []CachedItem {
	return redactItems(c.cache.List(includeItemsMarkedAsRead), c.googleAIAPIKeys)
}

// MarkCachedItemsAsRead marks given cached items as read.
func (c *Client) MarkCachedItemsAsRead(items []CachedItem) error {
	var errs []error
	for _, item := range items {
		if err := c.cache.MarkAsRead(item.GUID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DeleteOldCachedItems deletes old cached items, and cooldowns which expired
// long ago.
func (c *Client) DeleteOldCachedItems() error {
	return c.cache.DeleteOlderThan1Month()
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
				escaped := html.EscapeString(item.Comments)
				content += `<br><br>` + fmt.Sprintf(`Comments: <a href="%[1]s">%[1]s</a>`, escaped)
			} else {
				escaped := html.EscapeString(item.GUID)
				content += `<br><br>` + fmt.Sprintf(`GUID: <a href="%[1]s">%[1]s</a>`, escaped)
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
