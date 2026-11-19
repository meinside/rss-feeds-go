package rf

import (
	"io"
	"log"
	"os"
	"testing"
	"time"
)

// testVerbose is true when tests are run with `VERBOSE=true`.
var testVerbose = os.Getenv("VERBOSE") == "true"

// timeout of live API tests: the same as a feed item's in the library, so that
// a slow model which the library would wait for does not fail the tests
const liveTestTimeout = summarizeTimeoutSeconds * time.Second

// TestMain silences the package's log output (verbose messages, and errors
// which are logged instead of returned) unless `VERBOSE=true`, so that it does
// not interleave with the test results. Results of tests are reported with
// `t.Logf`, shown with `go test -v`.
func TestMain(m *testing.M) {
	if !testVerbose {
		log.SetOutput(io.Discard)
	}

	os.Exit(m.Run())
}
