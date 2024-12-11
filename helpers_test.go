package rf

import (
	"strings"
	"testing"
)

func TestHelpers(t *testing.T) {
	// test `getContentType`
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

	// test `decorateHTML`
	for original, expected := range map[string]string{
		"line 1\nline 2\n\nlast line\n":                     "line 1<br>line 2<br><br>last line<br>",
		"following **text** should be bolded! **":           "following <b>text</b> should be bolded! **",
		"text with <html tags> should be **escaped** first": "text with &lt;html tags&gt; should be <b>escaped</b> first",
		`**Summary failed with error: googleapi error: [{
  "error": {
    "code": 503,
    "message": "The model is overloaded. Please try again later.",
    "status": "UNAVAILABLE"
  }
}
]**`: `<b>Summary failed with error: googleapi error: [{<br>  "error": {<br>    "code": 503,<br>    "message": "The model is overloaded. Please try again later.",<br>    "status": "UNAVAILABLE"<br>  }<br>}<br>]</b>`,
	} {
		if decorated := decorateHTML(original); decorated != expected {
			t.Errorf("expected decorated html: '%s' vs actual: '%s'", expected, decorated)
		}
	}
}
