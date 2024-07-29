package rf

import (
	"strings"
	"testing"
)

func TestHelpers(t *testing.T) {
	// test `getContentType`
	urlsAndContentTypes := map[string]string{
		"https://github.com/meinside": "text/html",
		"https://raw.githubusercontent.com/meinside/meinside/main/res/profile/sloth.jpg": "image/jpeg",
	}
	for url, contentType := range urlsAndContentTypes {
		typ, err := getContentType(url, false)
		if err != nil {
			t.Errorf("failed to get content type of '%s': %s", url, err)
		}

		if !strings.HasPrefix(typ, contentType) {
			t.Errorf("expected content type: '%s' vs fetched: '%s'", contentType, typ)
		}
	}
}
