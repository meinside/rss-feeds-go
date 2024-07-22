package rf

import (
	"encoding/json"
	"errors"
	"fmt"
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

	redacted = "<<<REDACTED>>>"
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

// fetch the content from given url and convert it to text for prompting.
func urlToText(url string, verbose bool) (body string, err error) {
	client := &http.Client{
		Timeout: time.Duration(fetchURLTimeoutSeconds) * time.Second,
	}

	if verbose {
		log.Printf("[verbose] fetching from url: %s", url)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %s", err)
	}
	req.Header.Set("User-Agent", fakeUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch contents from url: %s", err)
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")

	if verbose {
		log.Printf("[verbose] fetched '%s' from url: %s", contentType, url)
	}

	if resp.StatusCode == 200 {
		if supportedHTTPContentType(contentType) {
			if strings.HasPrefix(contentType, "text/html") {
				var doc *goquery.Document
				if doc, err = goquery.NewDocumentFromReader(resp.Body); err == nil {
					// NOTE: removing unwanted things here
					_ = doc.Find("script").Remove()                   // javascripts
					_ = doc.Find("link[rel=\"stylesheet\"]").Remove() // css links
					_ = doc.Find("style").Remove()                    // embeded css tyles

					body = fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(doc.Text()))
				} else {
					body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this HTML document.")
					err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "text/") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					// (success)
					body = fmt.Sprintf(urlToTextFormat, url, contentType, removeConsecutiveEmptyLines(string(bytes))) // NOTE: removing redundant empty lines
				} else {
					body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document.")
					err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
				}
			} else if strings.HasPrefix(contentType, "application/json") {
				var bytes []byte
				if bytes, err = io.ReadAll(resp.Body); err == nil {
					body = fmt.Sprintf(urlToTextFormat, url, contentType, string(bytes))
				} else {
					body = fmt.Sprintf(urlToTextFormat, url, contentType, "Failed to read this document.")
					err = fmt.Errorf("failed to read '%s' document from %s: %s", contentType, url, err)
				}
			} else {
				body = fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType))
				err = fmt.Errorf("content type '%s' not supported for url: %s", contentType, url)
			}
		} else {
			body = fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("Content type '%s' not supported.", contentType))
			err = fmt.Errorf("content type '%s' not supported for url: %s", contentType, url)
		}
	} else {
		body = fmt.Sprintf(urlToTextFormat, url, contentType, fmt.Sprintf("HTTP Error %d", resp.StatusCode))
		err = fmt.Errorf("http error %d from url: %s", resp.StatusCode, url)
	}

	/*
		if verbose {
			log.Printf("[verbose] fetched body =\n%s\n", body)
		}
	*/

	return body, err
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

// check if given HTTP content type is supported
func supportedHTTPContentType(contentType string) bool {
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

// Prettify prettifies given thing in JSON format.
func Prettify(v any) string {
	if bytes, err := json.MarshalIndent(v, "", "  "); err == nil {
		return string(bytes)
	}
	return fmt.Sprintf("%+v", v)
}
