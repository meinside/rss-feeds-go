package rf

import (
	"context"
	"strings"
	"testing"
)

// test `getContentType`
func TestGetContentType(t *testing.T) {
	ctx := context.Background()

	for url, contentType := range map[string]string{
		"https://github.com/meinside": "text/html",
		"https://raw.githubusercontent.com/meinside/meinside/main/res/profile/sloth.jpg": "image/jpeg",
	} {
		typ, err := getContentType(ctx, url, false)
		if err != nil {
			t.Errorf("failed to get content type of '%s': %s", url, err)
		}

		if !strings.HasPrefix(typ, contentType) {
			t.Errorf("expected content type: '%s' vs fetched: '%s'", contentType, typ)
		}
	}
}

// test `decorateHTML`
func TestDecorateHTML(t *testing.T) {
	for original, expected := range map[string]string{
		"line 1\nline 2\n\nlast line\n":                     "line 1<br>line 2<br><br>last line<br>",
		"following **text** should be bolded! **":           "following <b>text</b> should be bolded! **",
		"text with <html tags> should be **escaped** first": "text with &lt;html tags&gt; should be <b>escaped</b> first",
		`**bold text
over multiple
lines**`: `<b>bold text<br>over multiple<br>lines</b>`,
	} {
		if decorated := decorateHTML(original); decorated != expected {
			t.Errorf("expected decorated html: '%s' vs actual: '%s'", expected, decorated)
		}
	}
}

// test `decorateHTML` with extended markdown patterns
func TestDecorateHTMLExtended(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// italic
		{
			"this is *italic* text",
			"this is <i>italic</i> text",
		},
		// inline code
		{
			"use `fmt.Println` here",
			"use <code>fmt.Println</code> here",
		},
		// link
		{
			"click [here](https://example.com) now",
			`click <a href="https://example.com">here</a> now`,
		},
		// bold + italic combined
		{
			"**bold** and *italic*",
			"<b>bold</b> and <i>italic</i>",
		},
		// multiple patterns
		{
			"**bold** with `code` and [link](https://x.com)",
			`<b>bold</b> with <code>code</code> and <a href="https://x.com">link</a>`,
		},
	}

	for _, tt := range tests {
		if got := decorateHTML(tt.input); got != tt.expected {
			t.Errorf("decorateHTML(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// test `isTextFormattableContent`
func TestIsTextFormattableContent(t *testing.T) {
	tests := []struct {
		contentType string
		expected    bool
	}{
		{"text/html; charset=utf-8", true},
		{"text/plain", true},
		{"text/xml", true},
		{"application/xhtml+xml", true},
		{"application/xml", true},
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/pdf", false},
		{"image/jpeg", false},
		{"video/mp4", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isTextFormattableContent(tt.contentType); got != tt.expected {
			t.Errorf("isTextFormattableContent(%q) = %v, want %v", tt.contentType, got, tt.expected)
		}
	}
}

// test `isFileContent`
func TestIsFileContent(t *testing.T) {
	tests := []struct {
		contentType string
		expected    bool
	}{
		{"application/pdf", true},
		{"application/pdf; charset=utf-8", true},
		{"text/html", false},
		{"image/jpeg", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isFileContent(tt.contentType); got != tt.expected {
			t.Errorf("isFileContent(%q) = %v, want %v", tt.contentType, got, tt.expected)
		}
	}
}

// test `isYouTubeURL`
func TestIsYouTubeURL(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"https://www.youtube.com/watch?v=abc123", true},
		{"https://youtu.be/abc123", true},
		{"https://www.youtube.com/live/abc123", true},
		{"https://www.youtube.com/playlist?list=abc123", false},
		{"https://youtu.be/playlist?list=abc123", false},
		{"https://github.com/meinside", false},
		{"https://example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isYouTubeURL(tt.url); got != tt.expected {
			t.Errorf("isYouTubeURL(%q) = %v, want %v", tt.url, got, tt.expected)
		}
	}
}

// test `normalizeYouTubeURL`
func TestNormalizeYouTubeURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://www.youtube.com/live/abc123", "https://www.youtube.com/watch?v=abc123"},
		{"https://www.youtube.com/watch?v=abc123", "https://www.youtube.com/watch?v=abc123"},
		{"https://youtu.be/abc123", "https://youtu.be/abc123"},
	}
	for _, tt := range tests {
		if got := normalizeYouTubeURL(tt.input); got != tt.expected {
			t.Errorf("normalizeYouTubeURL(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// test `redactText`
func TestRedactText(t *testing.T) {
	tests := []struct {
		text     string
		baddies  []string
		expected string
	}{
		{"my api key is SECRET123 here", []string{"SECRET123"}, "my api key is |REDACTED| here"},
		{"no secrets here", []string{"SECRET123"}, "no secrets here"},
		{"KEY1 and KEY2 exposed", []string{"KEY1", "KEY2"}, "|REDACTED| and |REDACTED| exposed"},
		{"", []string{"KEY"}, ""},
		{"some text", []string{}, "some text"},
	}
	for _, tt := range tests {
		if got := redactText(tt.text, tt.baddies); got != tt.expected {
			t.Errorf("redactText(%q, %v) = %q, want %q", tt.text, tt.baddies, got, tt.expected)
		}
	}
}

// test `redactItems`
func TestRedactItems(t *testing.T) {
	items := []CachedItem{
		{Summary: "contains SECRET_KEY in summary", GUID: "1"},
		{Summary: "", GUID: "2"},
		{Summary: "clean summary", GUID: "3"},
	}
	baddies := []string{"SECRET_KEY"}

	result := redactItems(items, baddies)

	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
	if strings.Contains(result[0].Summary, "SECRET_KEY") {
		t.Errorf("expected SECRET_KEY to be redacted, got %q", result[0].Summary)
	}
	if result[1].Summary != "" {
		t.Errorf("expected empty summary to remain empty, got %q", result[1].Summary)
	}
	if result[2].Summary != "clean summary" {
		t.Errorf("expected clean summary unchanged, got %q", result[2].Summary)
	}
}

// test `isError`
func TestIsError(t *testing.T) {
	tests := []struct {
		body     string
		expected bool
	}{
		{ErrorPrefixSummaryFailedWithError + ": some error", true},
		{"<p>" + ErrorPrefixSummaryFailedWithError + "</p>", true},
		{"normal summary content", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isError(tt.body); got != tt.expected {
			t.Errorf("isError(%q) = %v, want %v", tt.body, got, tt.expected)
		}
	}
}

// test `removeConsecutiveEmptyLines`
func TestRemoveConsecutiveEmptyLines(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"line1\n\n\n\nline2", "line1\nline2"},
		{"line1\nline2", "line1\nline2"},
		{"  trailing spaces  \n  more  ", "  trailing spaces\n  more"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := removeConsecutiveEmptyLines(tt.input); got != tt.expected {
			t.Errorf("removeConsecutiveEmptyLines(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// test `StandardizeJSON`
func TestStandardizeJSON(t *testing.T) {
	// JWCC with comments and trailing commas
	input := []byte(`{
		// this is a comment
		"key": "value",
		"arr": [1, 2, 3,],
	}`)
	result, err := StandardizeJSON(input)
	if err != nil {
		t.Fatalf("StandardizeJSON failed: %s", err)
	}
	if strings.Contains(string(result), "//") {
		t.Error("expected comments to be removed")
	}

	// invalid JSON
	_, err = StandardizeJSON([]byte(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// test `Prettify`
func TestPrettify(t *testing.T) {
	// struct
	result := Prettify(map[string]int{"a": 1})
	if !strings.Contains(result, `"a"`) {
		t.Errorf("expected prettified JSON, got %q", result)
	}

	// non-marshallable (channel)
	ch := make(chan int)
	result = Prettify(ch)
	if result == "" {
		t.Error("expected fallback format for non-marshallable type")
	}
}
