package rf

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/tailscale/hujson"
	"google.golang.org/api/googleapi"
)

const (
	urlToTextFormat = "<link url=\"%[1]s\" content-type=\"%[2]s\">\n%[3]s\n</link>"

	fakeUserAgent = `Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:128.0) Gecko/20100101 Firefox/128.0`

	fetchURLTimeoutSeconds = 10 // 10 seconds' timeout for fetching url contents

	redacted = "|REDACTED|"
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

// convert error to string
func errorString(err error) (error string) {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		return fmt.Sprintf("googleapi error: %s", gerr.Body)
	} else {
		return err.Error()
	}
}

// print verbose message
func v(verbose bool, format string, v ...any) {
	if verbose {
		log.Printf("[verbose] %s", fmt.Sprintf(format, v...))
	}
}

// get content type from given url with HTTP GET
func getContentType(url string, verbose bool) (contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(fetchURLTimeoutSeconds) * time.Second,
	}

	v(verbose, "fetching head from url: %s", url)

	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %s", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch head from url: %s", err)
	}
	defer resp.Body.Close()

	return resp.Header.Get("Content-Type"), nil
}

// fetch the content from given url and convert it for prompting.
func fetchURLContent(url string, verbose bool) (content []byte, contentType string, err error) {
	client := &http.Client{
		Timeout: time.Duration(fetchURLTimeoutSeconds) * time.Second,
	}

	v(verbose, "fetching contents from url: %s", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to create request: %s", err)
	}
	req.Header.Set("User-Agent", fakeUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, contentType, fmt.Errorf("failed to fetch contents from url: %s", err)
	}
	defer resp.Body.Close()

	contentType = resp.Header.Get("Content-Type")

	v(verbose, "fetched '%s' from url: %s", contentType, url)

	if resp.StatusCode == 200 {
		if isTextFormattableContent(contentType) { // then format as text prompt
			if strings.HasPrefix(contentType, "text/html") {
				var doc *goquery.Document
				if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
					// NOTE: removing unwanted things here
					_ = doc.Find("script").Remove()                   // javascripts
					_ = doc.Find("link[rel=\"stylesheet\"]").Remove() // css links
					_ = doc.Find("style").Remove()                    // embeded css tyles

					content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(doc.Text())))
				} else {
					content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this HTML document."))
					err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "text/") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					// (success)
					content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes)))) // NOTE: removing redundant empty lines
				} else {
					content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document."))
					err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "application/json") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, string(bytes)))
				} else {
					content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document."))
					err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
				}
			} else {
				content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType)))
				err = fmt.Errorf("content type '%s' not supported for url: %s", contentType, url)
			}
		} else if isFileContent(contentType) {
			if content, err = io.ReadAll(resp.Body); err != nil { // then read bytes as a file
				err = fmt.Errorf("failed to read bytes from url '%s': %s", url, err)
			}
		} else {
			content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType)))
			err = fmt.Errorf("content type '%s' not supported for url: %s", contentType, url)
		}
	} else {
		content = []byte(fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode)))
		err = fmt.Errorf("http error %d from url: %s", resp.StatusCode, url)
	}

	/*
		v(verbose, "fetched body = \n%s\n", body)
	*/

	return content, contentType, err
}

// remove consecutive empty lines for compacting prompt lines
func removeConsecutiveEmptyLines(input string) string {
	// trim each line
	trimmed := []string{}
	for _, line := range strings.Split(input, "\n") {
		trimmed = append(trimmed, strings.TrimRight(line, " "))
	}
	input = strings.Join(trimmed, "\n")

	// remove redundant empty lines
	regex := regexp.MustCompile("\n{2,}")
	return regex.ReplaceAllString(input, "\n")
}

// check if given HTTP content type is formattable as text for `fetchURL`
func isTextFormattableContent(contentType string) bool {
	return func(contentType string) bool {
		switch {
		case strings.HasPrefix(contentType, "text/"):
			return true
		case strings.HasPrefix(contentType, "application/json"):
			return true
		default:
			return false
		}
	}(contentType)
}

// check if given HTTP content type is used as file for `fetchURL`
func isFileContent(contentType string) bool {
	return func(contentType string) bool {
		switch {
		case strings.HasPrefix(contentType, "application/pdf"):
			return true
		default:
			return false
		}
	}(contentType)
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

	var summary string
	for _, item := range items {
		if len(item.Summary) > 0 {
			summary = redactText(item.Summary, baddies)
			item.Summary = summary
		}

		redacted = append(redacted, item)
	}

	return redacted
}

// check if given body string contains error prefix
func isError(body string) bool {
	return strings.Contains(body, ErrorPrefixSummaryFailedWithError)
}

// decorate given `body` as HTML
func decorateHTML(body string) string {
	// NOTE:
	// If `body` contains error prefix,
	// it has markdown-decorated text with the original text which is in HTML,
	// so it should not be escaped or replaced.
	// Otherwise, just escape or replace characters in `body` which was successfully generated by Gemini:
	if !isError(body) {
		// remove html tags from original generated content
		body = html.EscapeString(body)

		// convert "\n" to "<br>"
		body = strings.ReplaceAll(body, "\n", "<br>")
	}

	// convert "**something**" to "<b>something</b>"
	re := regexp.MustCompile(`\*\*(.*?)\*\*`)
	body = re.ReplaceAllString(body, `<b>$1</b>`)

	// TODO: add some more decorations

	return body
}

// Prettify prettifies given thing in JSON format.
func Prettify(v any) string {
	if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%+v", v)
}
