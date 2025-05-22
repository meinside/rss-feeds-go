package rf

import (
	"strings"
	"testing"
)

// test `getContentType`
func TestGetContentType(t *testing.T) {
	for url, contentType := range map[string]string{
		"https://github.com/meinside": "text/html",
		"https://raw.githubusercontent.com/meinside/meinside/main/res/profile/sloth.jpg": "image/jpeg",
	} {
		typ, err := getContentType(url, false)
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
