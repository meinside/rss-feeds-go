package rf

import (
	"strings"
	"testing"
)

func TestHelpers(t *testing.T) {
	kvs := map[string]string{
		"https://github.com/meinside": "text/html",
		"https://raw.githubusercontent.com/meinside/meinside/main/res/profile/sloth.jpg": "image/jpeg",
	}

	// test `getContentType`
	for k, v := range kvs {
		typ, err := getContentType(k, false)
		if err != nil {
			t.Errorf("failed to get content type of '%s': %s", k, err)
		}

		if !strings.HasPrefix(typ, v) {
			t.Errorf("expected content type: '%s' vs fetched: '%s'", v, typ)
		}
	}
}
