package rf

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
)

// responses in the shapes returned by the EXAONE API (ids are not real ones)
const (
	exaoneToolCallResponse    = `{"id":"test-response-id","choices":[{"finish_reason":"tool_calls","index":0,"logprobs":null,"message":{"content":"","refusal":null,"role":"assistant","function_call":null,"tool_calls":[{"id":"call_test","function":{"arguments":"{\"translatedTitle\": \"Go 1.26 출시\", \"summarizedContent\": \"Go 팀이 Go 1.26을 발표했습니다.\"}","name":"translateTitleAndSummarizeContent"},"type":"function","index":0}],"reasoning_content":"I'll use the tool for this."},"matched_stop":null}],"model":"k-exaone_v2.5","object":"chat.completion"}`
	exaoneContentResponse     = `{"id":"test-response-id","choices":[{"finish_reason":"stop","index":0,"message":{"content":"\n\n**요약:** Go 팀이 Go 1.26을 발표했습니다.","role":"assistant","tool_calls":null,"reasoning_content":"We need answer in Korean."}}],"model":"k-exaone_v2.5","object":"chat.completion"}`
	exaoneSecondLimitResponse = `{"error":{"message":"second limit(1), called 1 times per second","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}`
	exaoneServerErrorResponse = `{"error":{"message":"The service is currently experiencing a temporary issue. Please try again shortly.","type":"server_error","param":null,"code":null}}`
	exaoneInvalidKeyResponse  = `{"error":{"message":"invalid api key format","type":"authentication_error","param":null,"code":"invalid_api_key"}}`
)

// newExaoneTestClient returns a client with EXAONE api keys only, which
// requests given handler without waiting between requests.
func newExaoneTestClient(t *testing.T, handler http.HandlerFunc, apiKeys ...string) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c := NewClient(nil, nil)
	c.SetExaoneAPIKeys(apiKeys)
	c.exaone.baseURL = server.URL + "/v1"
	c.exaone.requestInterval = 0
	c.exaone.serverErrorBackoff = time.Millisecond

	return c
}

// writeExaoneResponse writes given status, headers, and body.
func writeExaoneResponse(w http.ResponseWriter, status int, body string, headers ...string) {
	for i := 0; i+1 < len(headers); i += 2 {
		w.Header().Set(headers[i], headers[i+1])
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// requested model of an exaone request
func exaoneRequestedModel(t *testing.T, r *http.Request) string {
	t.Helper()

	var req exaoneChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Errorf("failed to decode request: %s", err)
	}
	return req.Model
}

// the forced function call must be requested, and its arguments used
func TestExaoneTranslateAndSummarizeWithToolCall(t *testing.T) {
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer exaone-key" {
			t.Errorf("unexpected authorization header: %q", got)
		}

		var req exaoneChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %s", err)
			return
		}
		if req.Model != defaultExaoneModel {
			t.Errorf("model = %q, want %q", req.Model, defaultExaoneModel)
		}
		if req.ToolChoice == nil || req.ToolChoice.Function.Name != fnNameTranslateTitleAndSummarizeContent {
			t.Errorf("the function call is not forced: %+v", req.ToolChoice)
		}
		if len(req.Messages) != 2 || req.Messages[0].Role != "system" || req.Messages[1].Content != "the prompt" {
			t.Errorf("unexpected messages: %+v", req.Messages)
		}

		writeExaoneResponse(w, http.StatusOK, exaoneToolCallResponse)
	}, "exaone-key")

	usedModel, title, content, err := c.translateAndSummarize(context.Background(), "the prompt")
	if err != nil {
		t.Fatalf("expected success, got %s", err)
	}
	if usedModel != defaultExaoneModel {
		t.Errorf("usedModel = %q, want %q", usedModel, defaultExaoneModel)
	}
	if title != "Go 1.26 출시" {
		t.Errorf("title = %q", title)
	}
	if content != "Go 팀이 Go 1.26을 발표했습니다." {
		t.Errorf("content = %q", content)
	}
}

// without a function call, the content is used (and the reasoning is not)
func TestExaoneTranslateAndSummarizeWithContentOnly(t *testing.T) {
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeExaoneResponse(w, http.StatusOK, exaoneContentResponse)
	}, "exaone-key")

	_, title, content, err := c.translateAndSummarize(context.Background(), "the prompt")
	if err != nil {
		t.Fatalf("expected success, got %s", err)
	}
	if title != "" {
		t.Errorf("title = %q, want empty (the original title is kept by the caller)", title)
	}
	if content != "**요약:** Go 팀이 Go 1.26을 발표했습니다." {
		t.Errorf("content = %q", content)
	}
}

// an empty content (eg. reasoning used up the tokens) must be an error
func TestExaoneEmptyContentIsError(t *testing.T) {
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeExaoneResponse(w, http.StatusOK, `{"choices":[{"finish_reason":"length","message":{"content":"","tool_calls":null,"reasoning_content":"We need answer..."}}]}`)
	}, "exaone-key")

	_, _, _, err := c.translateAndSummarize(context.Background(), "the prompt")
	if err == nil || !strings.Contains(err.Error(), "length") {
		t.Errorf("expected an empty content error with the finish reason, got %v", err)
	}
}

// a 429 with a short retry-after must be waited out with the same combo, without
// a cooldown which would escalate to minutes
func TestExaoneRetriesShortRateLimitInPlace(t *testing.T) {
	var calls atomic.Int32
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeExaoneResponse(w, http.StatusTooManyRequests, exaoneSecondLimitResponse, "Retry-After", "1", "Retry-After-Ms", "10")
			return
		}
		writeExaoneResponse(w, http.StatusOK, exaoneToolCallResponse)
	}, "exaone-key")

	start := time.Now()
	if _, _, _, err := c.translateAndSummarize(context.Background(), "the prompt"); err != nil {
		t.Fatalf("expected success after a retry, got %s", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 calls, got %d", got)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Errorf("waited %s, but retry-after-ms (10ms) should be preferred over retry-after (1s)", elapsed)
	}
	if len(c.cooldownUntil) != 0 {
		t.Errorf("a short 429 must not cool the combo down, got %+v", c.cooldownUntil)
	}
}

// a 429 with a long retry-after must cool the combo down for that long, and fail
// over to another combo
func TestExaoneLongRateLimitCoolsDownAndFailsOver(t *testing.T) {
	var calls1, calls2 atomic.Int32
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer exaone-key-1" {
			calls1.Add(1)
			writeExaoneResponse(w, http.StatusTooManyRequests, `{"error":{"message":"daily limit exceeded","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}`, "Retry-After", "600")
			return
		}
		calls2.Add(1)
		writeExaoneResponse(w, http.StatusOK, exaoneToolCallResponse)
	}, "exaone-key-1", "exaone-key-2")

	before := time.Now()
	if _, _, _, err := c.translateAndSummarize(context.Background(), "the prompt"); err != nil {
		t.Fatalf("expected success after failover, got %s", err)
	}
	if got := calls1.Load(); got != 1 {
		t.Errorf("a long 429 must not be retried in place, got %d calls", got)
	}
	if got := calls2.Load(); got != 1 {
		t.Errorf("expected 1 call with the other key, got %d", got)
	}
	until, exists := c.cooldownUntil[0]
	if !exists {
		t.Fatalf("expected a cooldown of the 1st key, got %+v", c.cooldownUntil)
	}
	if until.Before(before.Add(600 * time.Second)) {
		t.Errorf("cooldown until %s is shorter than the retry-after (600s)", until)
	}
}

// 5xx must be retried with the same combo, and returned as a retriable error
// (without a cooldown) when it persists
func TestExaoneServerErrorIsRetriedThenReturned(t *testing.T) {
	var calls atomic.Int32
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeExaoneResponse(w, http.StatusInternalServerError, exaoneServerErrorResponse)
	}, "exaone-key")

	_, _, _, err := c.translateAndSummarize(context.Background(), "the prompt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != maxRetryCount+1 {
		t.Errorf("expected %d calls, got %d", maxRetryCount+1, got)
	}
	if !isRetriableLater(err) {
		t.Errorf("a persisting 5xx must be retriable later: %s", err)
	}
	if len(c.cooldownUntil) != 0 {
		t.Errorf("a 5xx must not cool the combo down, got %+v", c.cooldownUntil)
	}
}

// a persisting 5xx must fail over to another model
func TestExaoneServerErrorFailsOverToAnotherModel(t *testing.T) {
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if exaoneRequestedModel(t, r) == "exaone-m1" {
			writeExaoneResponse(w, http.StatusInternalServerError, exaoneServerErrorResponse)
			return
		}
		writeExaoneResponse(w, http.StatusOK, exaoneToolCallResponse)
	}, "exaone-key")
	c.SetExaoneModels([]string{"exaone-m1", "exaone-m2"})

	usedModel, _, _, err := c.translateAndSummarize(context.Background(), "the prompt")
	if err != nil {
		t.Fatalf("expected success after failover, got %s", err)
	}
	if usedModel != "exaone-m2" {
		t.Errorf("usedModel = %q, want exaone-m2", usedModel)
	}
}

// an invalid api key must be neither retried nor retriable later
func TestExaoneUnauthorizedIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeExaoneResponse(w, http.StatusUnauthorized, exaoneInvalidKeyResponse)
	}, "exaone-key")

	_, _, _, err := c.translateAndSummarize(context.Background(), "the prompt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 call, got %d", got)
	}
	if isRetriableLater(err) {
		t.Errorf("401 must not be retriable later: %s", err)
	}
	if !strings.Contains(err.Error(), "invalid_api_key") {
		t.Errorf("expected the error code in the message, got %q", err.Error())
	}
}

func TestNewExaoneAPIError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		headers    http.Header
		body       string
		wantCode   string
		wantType   string
		wantMsg    string
		wantRetry  time.Duration
		wantQuota  bool
		wantServer bool
	}{
		{
			name:      "per-second limit",
			status:    429,
			headers:   http.Header{"Retry-After": {"1"}, "Retry-After-Ms": {"874"}},
			body:      exaoneSecondLimitResponse,
			wantCode:  "rate_limit_exceeded",
			wantType:  "rate_limit_error",
			wantMsg:   "second limit(1), called 1 times per second",
			wantRetry: 874 * time.Millisecond,
			wantQuota: true,
		},
		{
			name:      "per-minute service limit, retry-after only",
			status:    429,
			headers:   http.Header{"Retry-After": {"7"}},
			body:      `{"error":{"message":"service limit(600) exceeded per minute","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}`,
			wantCode:  "rate_limit_exceeded",
			wantType:  "rate_limit_error",
			wantMsg:   "service limit(600) exceeded per minute",
			wantRetry: 7 * time.Second,
			wantQuota: true,
		},
		{
			name:       "server error with a null code",
			status:     500,
			body:       exaoneServerErrorResponse,
			wantType:   "server_error",
			wantMsg:    "The service is currently experiencing a temporary issue. Please try again shortly.",
			wantServer: true,
		},
		{
			name:       "documented platform style",
			status:     500,
			body:       `{"result_code": 500, "description": "temporary issue"}`,
			wantCode:   "500",
			wantMsg:    "temporary issue",
			wantServer: true,
		},
		{
			name:    "not json",
			status:  404,
			body:    "Not Found",
			wantMsg: "Not Found",
		},
		{
			name:       "empty body",
			status:     502,
			wantMsg:    "Bad Gateway",
			wantServer: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := tt.headers
			if headers == nil {
				headers = http.Header{}
			}
			e := newExaoneAPIError(tt.status, headers, []byte(tt.body))

			if e.StatusCode != tt.status || e.Code != tt.wantCode || e.Type != tt.wantType || e.Message != tt.wantMsg {
				t.Errorf("got %+v", e)
			}
			if e.RetryAfter != tt.wantRetry {
				t.Errorf("RetryAfter = %s, want %s", e.RetryAfter, tt.wantRetry)
			}
			if got := isQuotaError(e); got != tt.wantQuota {
				t.Errorf("isQuotaError() = %v, want %v", got, tt.wantQuota)
			}
			if got := isExaoneServerError(e); got != tt.wantServer {
				t.Errorf("isExaoneServerError() = %v, want %v", got, tt.wantServer)
			}
		})
	}
}

// a cooldown must honor the retry-after of an EXAONE 429
func TestCooldownDurationHonorsExaoneRetryAfter(t *testing.T) {
	err := &exaoneAPIError{StatusCode: 429, RetryAfter: 30 * time.Second}

	got := cooldownDuration(err, 1, time.Now())
	if got < 30*time.Second || got > time.Duration(float64(30*time.Second)*(1+cooldownJitterRatio)) {
		t.Errorf("cooldown = %s, want 30s + jitter", got)
	}
}

// files (and youtube videos, url context) can only be done by Gemini: with
// EXAONE api keys only, it must fail without a request, and not be retriable
func TestExaoneOnlyClientRejectsGeminiOnlyTasks(t *testing.T) {
	var calls atomic.Int32
	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeExaoneResponse(w, http.StatusOK, exaoneToolCallResponse)
	}, "exaone-key")

	_, _, _, err := c.translateAndSummarize(context.Background(), "the prompt", []byte("%PDF-1.7"))
	if !errors.Is(err, ErrNoProviderForTask) {
		t.Errorf("expected ErrNoProviderForTask, got %v", err)
	}
	if isRetriableLater(err) {
		t.Errorf("ErrNoProviderForTask must not be retriable later")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("expected no request, got %d", got)
	}
}

// with EXAONE api keys only, a PDF item must be cached with the error message
// and the original content, as any other failed summary
func TestSummarizeAndCacheFeedsCachesUnsupportedContentWithExaoneOnly(t *testing.T) {
	pdfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.7"))
	}))
	defer pdfServer.Close()

	c := newExaoneTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no exaone request is expected")
	}, "exaone-key")
	c.SetSummarizeIntervalSeconds(0)

	items := []*gofeed.Item{
		{GUID: "guid-pdf", Title: "A PDF", Link: pdfServer.URL, Links: []string{pdfServer.URL}, Description: "desc pdf"},
	}

	err := c.SummarizeAndCacheFeeds(context.Background(), []gofeed.Feed{{Items: items}})
	if !errors.Is(err, ErrNoProviderForTask) {
		t.Errorf("expected ErrNoProviderForTask in the chain, got %v", err)
	}

	cached := c.cache.Fetch("guid-pdf")
	if cached == nil {
		t.Fatal("the item must be cached")
	}
	if !isError(cached.Summary) || !strings.Contains(cached.Summary, "desc pdf") {
		t.Errorf("expected the error and the original content, got %q", cached.Summary)
	}
}

// EXAONE combos come after Gemini's, and are never picked for Gemini-only tasks
func TestBuildCombosWithExaone(t *testing.T) {
	c := NewClient([]string{"gemini-key"}, nil)
	c.SetGoogleAIModels([]string{"gemini-m1"})
	c.SetExaoneAPIKeys([]string{"exaone-key"})

	if len(c.combos) != 2 {
		t.Fatalf("expected 2 combos, got %d", len(c.combos))
	}
	if c.combos[0].provider != providerGemini || c.combos[1].provider != providerExaone || c.combos[1].model != defaultExaoneModel {
		t.Errorf("unexpected combos: %+v", c.combos)
	}

	now := time.Unix(1_000_000, 0)

	// text: round-robin over both providers
	seen := map[provider]bool{}
	for range 2 {
		combo, _, ok := c.pickAvailableComboFor(taskText, now, nil)
		if !ok {
			t.Fatal("expected ok")
		}
		seen[combo.provider] = true
	}
	if !seen[providerGemini] || !seen[providerExaone] {
		t.Errorf("text tasks did not round-robin over both providers: %+v", seen)
	}

	// gemini-only: never exaone
	for range 4 {
		combo, _, ok := c.pickAvailableComboFor(taskGeminiOnly, now, nil)
		if !ok {
			t.Fatal("expected ok")
		}
		if combo.provider != providerGemini {
			t.Errorf("picked %+v for a gemini-only task", combo)
		}
	}
}

// EXAONE api keys must be redacted too
func TestListCachedItemsRedactsExaoneAPIKeys(t *testing.T) {
	c := NewClient([]string{"gemini-secret"}, nil)
	c.SetExaoneAPIKeys([]string{"exaone-secret"})

	_ = c.cache.Save(testFeedItem("guid-redact", "Title"), "Title", "leaked exaone-secret and gemini-secret")

	for _, item := range c.ListCachedItems(true) {
		if strings.Contains(item.Summary, "exaone-secret") || strings.Contains(item.Summary, "gemini-secret") {
			t.Errorf("api key not redacted: %q", item.Summary)
		}
	}
}

// requests with the same api key must keep the interval
func TestWaitForExaoneRequestSlotKeepsInterval(t *testing.T) {
	c := NewClient(nil, nil)
	c.exaone.requestInterval = 50 * time.Millisecond

	start := time.Now()
	for range 3 {
		if err := c.waitForExaoneRequestSlot(context.Background(), "key-hash"); err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("3 requests took %s, want >= 100ms", elapsed)
	}

	// a canceled context stops the wait
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.exaone.requestInterval = time.Hour
	_ = c.waitForExaoneRequestSlot(context.Background(), "other-key-hash")
	if err := c.waitForExaoneRequestSlot(ctx, "other-key-hash"); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// timeout of live EXAONE tests: a generation takes ~5-15s, and 5xx/429 are
// retried in place
const exaoneLiveTestTimeout = 3 * time.Minute

// helper for creating a client with EXAONE API key (only) from env
func newTestClientWithExaoneAPI(t *testing.T) *Client {
	t.Helper()

	apiKey := os.Getenv("EXAONE_API_KEY")
	if apiKey == "" {
		t.Skip("EXAONE_API_KEY not set")
	}

	client := NewClient(nil, nil)
	client.SetExaoneAPIKeys([]string{apiKey})
	if model := os.Getenv("EXAONE_MODEL"); model != "" {
		client.SetExaoneModels([]string{model})
	}
	client.SetDesiredLanguage("Korean")
	client.SetVerbose(true)

	return client
}

// test summarize content and translate title with the EXAONE API
func TestSummarizeURLContentWithExaone(t *testing.T) {
	client := newTestClientWithExaoneAPI(t)

	ctx, cancel := context.WithTimeout(context.TODO(), exaoneLiveTestTimeout)
	defer cancel()

	usedModel, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		`meinside/rss-feeds-go: A go utility package for handling RSS feeds.`,
		`https://github.com/meinside/rss-feeds-go`,
	)
	if err != nil {
		t.Errorf("failed to summarize url content with exaone: %s", err)
	} else {
		if len(strings.TrimSpace(summarizedContent)) == 0 {
			t.Error("summarized content is empty")
		}
		log.Printf(">>> [exaone] used model: %s", usedModel)
		log.Printf(">>> [exaone] translated title: %s", translatedTitle)
		log.Printf(">>> [exaone] summarized content: %s", summarizedContent)
	}
}

// test summarize youtube url with the EXAONE API only (should fail without a
// request, as only Gemini can summarize videos)
func TestSummarizeYouTubeWithExaone(t *testing.T) {
	client := newTestClientWithExaoneAPI(t)

	ctx, cancel := context.WithTimeout(context.TODO(), exaoneLiveTestTimeout)
	defer cancel()

	title := `I2C test on Raspberry Pi with Adafruit 8x8 LED Matrix and Ruby`
	_, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		title,
		`https://www.youtube.com/watch?v=fV5rI_5fDI8`,
	)
	if !errors.Is(err, ErrNoProviderForTask) {
		t.Errorf("expected ErrNoProviderForTask, got %v", err)
	}
	if translatedTitle != title {
		t.Errorf("should have kept the title, but got '%s'", translatedTitle)
	}
	log.Printf(">>> [exaone, youtube] summarized content (error message): %s", summarizedContent)
}

// test summarize with wrong url with the EXAONE API only (should fail, as the
// fallback with url context is done by Gemini only)
func TestSummarizeWrongURLWithExaone(t *testing.T) {
	client := newTestClientWithExaoneAPI(t)

	ctx, cancel := context.WithTimeout(context.TODO(), exaoneLiveTestTimeout)
	defer cancel()

	title := `What is the answer to life, the universe, and everything?`
	_, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		title,
		`https://no-sucn-domain/that-will-lead/to/fetch-error`,
	)
	if !errors.Is(err, ErrNoProviderForTask) {
		t.Errorf("expected ErrNoProviderForTask, got %v", err)
	}
	if isRetriableLater(err) {
		t.Errorf("should not be retriable later: %v", err)
	}
	if translatedTitle != title {
		t.Errorf("should have kept the title, but got '%s'", translatedTitle)
	}
	log.Printf(">>> [exaone, wrong url] summarized content (error message): %s", summarizedContent)
}

// test summarize with wrong EXAONE API key (should fail and keep original title)
func TestSummarizeWrongAPIKeyWithExaone(t *testing.T) {
	if os.Getenv("EXAONE_API_KEY") == "" {
		t.Skip("EXAONE_API_KEY not set")
	}

	client := NewClient(nil, nil)
	client.SetExaoneAPIKeys([]string{"intentionally-wrong-api-key"})
	client.SetDesiredLanguage("Korean")

	ctx, cancel := context.WithTimeout(context.TODO(), exaoneLiveTestTimeout)
	defer cancel()

	// NOTE: a fetchable url, so that the exaone api is actually requested
	title := `meinside/rss-feeds-go: A go utility package for handling RSS feeds.`
	_, translatedTitle, summarizedContent, err := client.summarize(
		ctx,
		title,
		`https://github.com/meinside/rss-feeds-go`,
	)
	if err == nil {
		t.Fatal("should have failed with the wrong api key")
	}
	var exaoneErr *exaoneAPIError
	if !errors.As(err, &exaoneErr) || exaoneErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected an http 401 from the exaone api, got %v", err)
	}
	if isRetriableLater(err) {
		t.Errorf("should not be retriable later: %v", err)
	}
	if translatedTitle != title {
		t.Errorf("should have kept the title, but got '%s'", translatedTitle)
	}
	log.Printf(">>> [exaone, api error] summarized content (api error): %s", summarizedContent)
}

// test summarize content with both Gemini and EXAONE API keys (needs both
// GEMINI_API_KEY and EXAONE_API_KEY): text contents are round-robined over
// both providers, and fail over to each other
func TestSummarizeURLContentWithGeminiAndExaone(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY not set")
	}
	client := newTestClientWithAPI(t)

	exaoneAPIKey := os.Getenv("EXAONE_API_KEY")
	if exaoneAPIKey == "" {
		t.Skip("EXAONE_API_KEY not set")
	}
	client.SetExaoneAPIKeys([]string{exaoneAPIKey})
	client.SetVerbose(true)

	// NOTE: one call per combo, so that each provider gets its turn
	for i := range len(client.combos) {
		ctx, cancel := context.WithTimeout(context.TODO(), exaoneLiveTestTimeout)
		usedModel, translatedTitle, summarizedContent, err := client.summarize(
			ctx,
			`meinside/rss-feeds-go: A go utility package for handling RSS feeds.`,
			`https://github.com/meinside/rss-feeds-go`,
		)
		cancel()

		if err != nil {
			t.Errorf("[%d] failed to summarize url content: %s", i, err)
			continue
		}
		log.Printf(">>> [mixed %d] used model: %s", i, usedModel)
		log.Printf(">>> [mixed %d] translated title: %s", i, translatedTitle)
		log.Printf(">>> [mixed %d] summarized content: %s", i, summarizedContent)
	}
}
