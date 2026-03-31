package rf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/tailscale/hujson"
	"github.com/yuin/goldmark"
)

const (
	urlToTextFormat = "<link url=\"%[1]s\" content-type=\"%[2]s\">\n%[3]s\n</link>"

	fakeUserAgent = `Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:147.0) Gecko/20100101 Firefox/147.0`
	fakeAccept    = `text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8`

	fetchURLTimeoutSeconds = 10 // timeout seconds for fetching url contents

	redacted = "|REDACTED|"
)

var (
	reConsecutiveEmptyLines = regexp.MustCompile(`\n{2,}`)
)

// StandardizeJSON standardizes given JSON (JWCC) bytes.
func StandardizeJSON(b []byte) ([]byte, error) {
	ast, err := hujson.Parse(b)
	if err != nil {
		return b, err
	}
	ast.Standardize()

	return ast.Pack(), nil
}

// print verbose message
func v(verbose bool, format string, v ...any) {
	if verbose {
		log.Printf("[verbose] %s", fmt.Sprintf(format, v...))
	}
}

// get content type from given url with HTTP HEAD
func getContentType(ctx context.Context, url string, verbose bool) (contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(fetchURLTimeoutSeconds) * time.Second,
	}

	v(verbose, "fetching head from url: %s", url)

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch head from url: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.Header.Get("Content-Type"), nil
}

// fetch the content from given url and convert it for prompting.
func fetchURLContent(ctx context.Context, url string, verbose bool) (content []byte, contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(fetchURLTimeoutSeconds) * time.Second,
	}

	v(verbose, "fetching contents from url: %s", url)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set(`User-Agent`, fakeUserAgent)
	req.Header.Set(`Accept`, fakeAccept)
	req.Header.Set(`Cache-Control`, `no-cache`)
	req.Header.Set(`Sec-Fetch-Dest`, `document`)
	req.Header.Set(`Sec-Fetch-Mode`, `navigate`)
	req.Header.Set(`Sec-Fetch-Site`, `none`)
	req.Header.Set(`Sec-Fetch-User`, `?1`)

	resp, err := client.Do(req)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to fetch contents from url: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	contentType = resp.Header.Get("Content-Type")

	v(verbose, "fetched '%s' from url: %s", contentType, url)

	if resp.StatusCode == 200 {
		if isTextFormattableContent(contentType) { // then format as text prompt
			if strings.HasPrefix(contentType, "text/html") ||
				strings.HasPrefix(contentType, "application/xhtml") ||
				strings.HasPrefix(contentType, "application/xml") {
				var doc *goquery.Document
				if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
					// NOTE: removing unwanted things here
					_ = doc.Find("script").Remove()                   // javascripts
					_ = doc.Find("link[rel=\"stylesheet\"]").Remove() // css links
					_ = doc.Find("style").Remove()                    // embeded css tyles

					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(doc.Text()))
				} else {
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, "Failed to read this HTML document.")
					err = fmt.Errorf("failed to read '%s' document from '%s': %w", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "text/") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					// NOTE: removing redundant empty lines
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes)))
				} else {
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, "Failed to read this document.")
					err = fmt.Errorf("failed to read '%s' document from '%s': %w", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "application/json") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, string(bytes))
				} else {
					content = fmt.Appendf(nil, urlToTextFormat, url, contentType, "Failed to read this document.")
					err = fmt.Errorf("failed to read '%s' document from '%s': %w", contentType, url, err)
				}
			} else {
				content = fmt.Appendf(nil, urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType))
				err = fmt.Errorf("content type '%s' not supported for url: '%s'", contentType, url)
			}
		} else if isFileContent(contentType) {
			if content, err = io.ReadAll(resp.Body); err != nil { // then read bytes as a file
				err = fmt.Errorf("failed to read bytes from url '%s': %w", url, err)
			}
		} else {
			content = fmt.Appendf(nil, urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType))
			err = fmt.Errorf("content type '%s' not supported for url: '%s'", contentType, url)
		}
	} else {
		content = fmt.Appendf(nil, urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode))
		err = fmt.Errorf("http error %d from url: '%s'", resp.StatusCode, url)
	}

	return content, contentType, err
}

// remove consecutive empty lines for compacting prompt lines
func removeConsecutiveEmptyLines(input string) string {
	// trim each line
	trimmed := []string{}
	for line := range strings.SplitSeq(input, "\n") {
		trimmed = append(trimmed, strings.TrimRight(line, " "))
	}
	input = strings.Join(trimmed, "\n")

	// remove redundant empty lines
	return reConsecutiveEmptyLines.ReplaceAllString(input, "\n")
}

// check if given HTTP content type is formattable as text for `fetchURL`
func isTextFormattableContent(contentType string) bool {
	switch {
	case strings.HasPrefix(contentType, "text/"):
		return true
	case strings.HasPrefix(contentType, "application/xhtml") ||
		strings.HasPrefix(contentType, "application/xml"):
		return true
	case strings.HasPrefix(contentType, "application/json"):
		return true
	default:
		return false
	}
}

// check if given HTTP content type is used as file for `fetchURL`
func isFileContent(contentType string) bool {
	return strings.HasPrefix(contentType, "application/pdf")
}

// redact given string not to expose api keys or etc.
func redactText(text string, baddies []string) string {
	for _, baddy := range baddies {
		text = strings.ReplaceAll(text, baddy, redacted)
	}

	return text
}

// redact cached items' summary (to prevent accidental exposure of api keys)
func redactItems(items []CachedItem, baddies []string) []CachedItem {
	redacted := []CachedItem{}

	for _, item := range items {
		if len(item.Summary) > 0 {
			item.Summary = redactText(item.Summary, baddies)
		}

		redacted = append(redacted, item)
	}

	return redacted
}

// check if given body string contains error prefix
func isError(body string) bool {
	return strings.Contains(body, ErrorPrefixSummaryFailedWithError)
}

// decorate given `body` as HTML with markdown conversion
func decorateHTML(body string) string {
	// NOTE:
	// If `body` contains error prefix, it already has raw HTML mixed in
	// (error message + original content), so return it as-is.
	if isError(body) {
		return body
	}

	var buf bytes.Buffer
	md := goldmark.New()
	if err := md.Convert([]byte(body), &buf); err == nil {
		return buf.String()
	}

	// fallback: simple escaping if goldmark fails
	body = html.EscapeString(body)
	body = strings.ReplaceAll(body, "\n", "<br>")
	return body
}

// check if given URL is a YouTube video
func isYouTubeURL(url string) bool {
	return slices.ContainsFunc([]string{
		"www.youtube.com",
		"youtu.be",
	}, func(e string) bool {
		// NOTE: ignore youtube playlists
		if strings.Contains(url, "/playlist") {
			return false
		}

		return strings.Contains(url, e)
	})
}

// normalize given YouTube URL for summary
func normalizeYouTubeURL(url string) string {
	if strings.HasPrefix(url, "https://www.youtube.com/live/") {
		return strings.ReplaceAll(url, "https://www.youtube.com/live/", "https://www.youtube.com/watch?v=")
	}
	return url
}

// Prettify prettifies given thing in JSON format.
func Prettify(v any) string {
	if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%+v", v)
}
