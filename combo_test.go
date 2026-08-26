package rf

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	gt "github.com/meinside/gemini-things-go"
	"google.golang.org/genai"
)

func TestBuildCombos(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1", "m2", "m3"})

	if len(c.combos) != 6 {
		t.Fatalf("expected 6 combos, got %d", len(c.combos))
	}

	// combos are ordered (k1,m1) first ... (k2,m3) last (key outer loop, model inner loop)
	if c.combos[0].apiKey != "k1" || c.combos[0].model != "m1" {
		t.Errorf("combos[0] = %+v", c.combos[0])
	}
	if c.combos[5].apiKey != "k2" || c.combos[5].model != "m3" {
		t.Errorf("combos[5] = %+v", c.combos[5])
	}

	// re-calling SetGoogleAIModels resets cooldownUntil
	c.cooldownUntil[0] = timeNowForTest()
	c.SetGoogleAIModels([]string{"m1"})
	if len(c.cooldownUntil) != 0 {
		t.Errorf("cooldownUntil not reset, len=%d", len(c.cooldownUntil))
	}
	if len(c.combos) != 2 {
		t.Errorf("expected 2 combos after reset, got %d", len(c.combos))
	}
}

func timeNowForTest() time.Time { return time.Unix(1_000_000, 0) }

func TestPickAvailableComboRoundRobin(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // combos: [(k1,m1),(k2,m1)]

	now := time.Unix(1_000_000, 0)

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		combo, _, ok := c.pickAvailableCombo(now, nil)
		if !ok {
			t.Fatalf("expected ok on iteration %d", i)
		}
		got[combo.apiKey] = true
	}
	if !got["k1"] || !got["k2"] {
		t.Errorf("round-robin did not cover both keys: %+v", got)
	}
}

func TestPickAvailableComboSkipsCooldown(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // idx0=(k1,m1), idx1=(k2,m1)

	now := time.Unix(1_000_000, 0)
	c.cooldownUntil[0] = now.Add(30 * time.Second) // put k1 combo in cooldown

	for i := 0; i < 3; i++ {
		combo, idx, ok := c.pickAvailableCombo(now, nil)
		if !ok {
			t.Fatalf("expected ok, iteration %d", i)
		}
		if idx == 0 || combo.apiKey != "k2" {
			t.Errorf("expected to always pick k2 (idx1), got idx=%d key=%s", idx, combo.apiKey)
		}
	}
}

func TestPickAvailableComboAllCooldown(t *testing.T) {
	c := NewClient([]string{"k1"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // single combo

	now := time.Unix(1_000_000, 0)
	c.cooldownUntil[0] = now.Add(30 * time.Second)

	if _, _, ok := c.pickAvailableCombo(now, nil); ok {
		t.Errorf("expected ok=false when all combos in cooldown")
	}

	// available again after cooldown expires
	later := now.Add(31 * time.Second)
	if _, _, ok := c.pickAvailableCombo(later, nil); !ok {
		t.Errorf("expected ok=true after cooldown expired")
	}
}

// combos whose model is skipped must not be picked (503 fails over to another
// model only, never to another api key for the same overloaded model)
func TestPickAvailableComboSkipsModels(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1", "m2"}) // 4 combos, 2 per model

	now := time.Unix(1_000_000, 0)
	skip := map[string]bool{"m1": true}

	for i := 0; i < 4; i++ {
		combo, _, ok := c.pickAvailableCombo(now, skip)
		if !ok {
			t.Fatalf("expected ok on iteration %d", i)
		}
		if combo.model != "m2" {
			t.Errorf("expected model m2, got %q", combo.model)
		}
	}

	// every model skipped -> nothing left
	if _, _, ok := c.pickAvailableCombo(now, map[string]bool{"m1": true, "m2": true}); ok {
		t.Error("expected ok=false when every model is skipped")
	}
}

// 503 must fail over to a different model, and must not waste a call on another
// api key for the same overloaded model
func TestWithFailoverOverloadedModelSkipsSameModel(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1", "m2"}) // 4 combos

	now := time.Unix(1_000_000, 0)
	nowFn := func() time.Time { return now }

	overloaded := genai.APIError{Code: 503, Message: "The model is overloaded. Please try again later."}

	triedModels := []string{}
	usedModel, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		triedModels = append(triedModels, model)
		if model == triedModels[0] {
			return overloaded // the first model tried is overloaded
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after failing over to another model, got %v", err)
	}
	if len(triedModels) != 2 {
		t.Fatalf("expected 2 calls (one per model), got %d: %v", len(triedModels), triedModels)
	}
	if triedModels[0] == triedModels[1] {
		t.Errorf("expected a different model on failover, tried %v", triedModels)
	}
	if usedModel != triedModels[1] {
		t.Errorf("usedModel = %q, want %q", usedModel, triedModels[1])
	}
	// 503 is not a quota error, so no cooldown must be recorded
	if len(c.cooldownUntil) != 0 {
		t.Errorf("503 must not mark cooldown, got %d", len(c.cooldownUntil))
	}
}

// with a single model there is nowhere to fail over to: the 503 itself must be
// returned (so that the caller leaves the remaining items for a later run)
func TestWithFailoverOverloadedSingleModel(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // 2 combos, same model

	now := time.Unix(1_000_000, 0)
	nowFn := func() time.Time { return now }

	overloaded := genai.APIError{Code: 503, Message: "The model is overloaded. Please try again later."}

	calls := 0
	_, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		calls++
		return overloaded
	})
	if calls != 1 {
		t.Errorf("expected 1 call (no other model to try), got %d", calls)
	}
	if !gt.IsModelOverloaded(err) {
		t.Errorf("expected the 503 to be returned, got %v", err)
	}
	if !isRetriableLater(err) {
		t.Error("the returned 503 must be retriable later")
	}
}

func TestParseRetryDelay(t *testing.T) {
	tests := []struct {
		name    string
		details []map[string]any
		want    time.Duration
		wantOK  bool
	}{
		{
			name: "valid retry info",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "37s"},
			},
			want:   37 * time.Second,
			wantOK: true,
		},
		{
			name: "fractional seconds",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "1.5s"},
			},
			want:   1500 * time.Millisecond,
			wantOK: true,
		},
		{
			name:    "nil details -> not ok",
			details: nil,
		},
		{
			name: "no retry info -> not ok",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.QuotaFailure"},
			},
		},
		{
			name: "unparsable delay -> not ok",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "soon"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRetryDelay(tt.details)
			if ok != tt.wantOK {
				t.Errorf("parseRetryDelay() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("parseRetryDelay() = %v, want %v", got, tt.want)
			}
		})
	}
}

// cooldowns must grow with consecutive failures of the same combo, instead of
// staying at `defaultCooldownSeconds` and hammering an exhausted quota once a minute
func TestCooldownDurationEscalates(t *testing.T) {
	base := time.Duration(defaultCooldownSeconds) * time.Second
	maxJitter := 1 + cooldownJitterRatio
	now := time.Now()

	// no RetryInfo: base, doubling with each consecutive failure
	var prev time.Duration
	for failures := 1; failures <= 5; failures++ {
		floor := base << (failures - 1)
		got := cooldownDuration(errors.New("Error 429 : exceeded your current quota"), failures, now)

		if got < floor {
			t.Errorf("failures=%d: cooldown %v < floor %v", failures, got, floor)
		}
		if got > time.Duration(float64(floor)*maxJitter) {
			t.Errorf("failures=%d: cooldown %v exceeds floor %v + jitter", failures, got, floor)
		}
		if got <= prev {
			t.Errorf("failures=%d: cooldown %v did not grow over %v", failures, got, prev)
		}
		prev = got
	}

	// escalation stops at the cap
	capped := time.Duration(maxEscalatedCooldownSeconds) * time.Second
	if got := cooldownDuration(errors.New("Error 429"), maxCooldownFailures+10, now); got > time.Duration(float64(capped)*maxJitter) {
		t.Errorf("escalated cooldown %v exceeds cap %v + jitter", got, capped)
	}
}

// a server-provided retryDelay is honored on the first failure, and is never
// shortened by the escalation afterwards
func TestCooldownDurationHonorsRetryDelay(t *testing.T) {
	details := []map[string]any{
		{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "3600s"},
	}
	err := genai.APIError{Code: 429, Message: "exceeded your current quota", Details: details}

	for _, failures := range []int{1, 2, 5} {
		got := cooldownDuration(err, failures, time.Now())
		if got < time.Hour {
			t.Errorf("failures=%d: cooldown %v is shorter than the server's 1h retryDelay", failures, got)
		}
	}
}

// a successful call must clear both the cooldown and the failure count of the combo
func TestWithFailoverClearsCooldownOnSuccess(t *testing.T) {
	c := NewClient([]string{"k1"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // single combo

	now := time.Unix(1_000_000, 0)
	nowFn := func() time.Time { return now }

	c.cooldownFailures[0] = 3

	if _, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		return nil
	}); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	if len(c.cooldownFailures) != 0 {
		t.Errorf("expected failure count to be cleared, got %+v", c.cooldownFailures)
	}
	if len(c.cooldownUntil) != 0 {
		t.Errorf("expected cooldown to be cleared, got %+v", c.cooldownUntil)
	}
}

func quotaErrForTest() error {
	return errors.New("Error 429 : exceeded your current quota, please check your plan")
}

func TestWithFailoverRetriesOnQuota(t *testing.T) {
	c := NewClient([]string{"k1", "k2", "k3"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // 3 combos

	now := time.Unix(1_000_000, 0)
	nowFn := func() time.Time { return now }

	calls := 0
	usedModel, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		calls++
		if calls < 3 {
			return quotaErrForTest()
		}
		return nil // succeeds on the 3rd attempt
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
	if usedModel != "m1" {
		t.Errorf("usedModel = %q", usedModel)
	}
	// the first 2 combos should be marked as cooling down
	if len(c.cooldownUntil) != 2 {
		t.Errorf("expected 2 cooled-down combos, got %d", len(c.cooldownUntil))
	}
}

func TestWithFailoverAllQuota(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // 2 combos

	now := time.Unix(1_000_000, 0)
	nowFn := func() time.Time { return now }

	_, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		return quotaErrForTest()
	})
	if !errors.Is(err, ErrNoAvailableAPIKey) {
		t.Errorf("expected ErrNoAvailableAPIKey, got %v", err)
	}
}

func TestWithFailoverNonQuotaErrorReturnsImmediately(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1"})

	now := time.Unix(1_000_000, 0)
	nowFn := func() time.Time { return now }

	sentinel := errors.New("boom")
	calls := 0
	_, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no failover on non-429), got %d", calls)
	}
	if len(c.cooldownUntil) != 0 {
		t.Errorf("non-429 must not mark cooldown, got %d", len(c.cooldownUntil))
	}
}

// 429s worded differently from what `gt.IsQuotaExceeded` looks for must still
// be treated as quota errors
func TestIsQuotaError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "429 with the expected message",
			err:  genai.APIError{Code: 429, Message: "You exceeded your current quota"},
			want: true,
		},
		{
			name: "429 worded differently",
			err:  genai.APIError{Code: 429, Message: "Resource has been exhausted (e.g. check quota)."},
			want: true,
		},
		{
			name: "wrapped 429",
			err:  fmt.Errorf("failed to generate: %w", genai.APIError{Code: 429, Message: "Too many requests"}),
			want: true,
		},
		{
			name: "stringified 429 (legacy form)",
			err:  errors.New("Error 429 : exceeded your current quota, please check your plan"),
			want: true,
		},
		{
			name: "503",
			err:  genai.APIError{Code: 503, Message: "The model is overloaded."},
		},
		{
			name: "non-api error",
			err:  errors.New("boom"),
		},
		{
			name: "nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isQuotaError(tt.err); got != tt.want {
				t.Errorf("isQuotaError(%v) = %v, want %v", tt.err, got, tt.want)
			}
			// and such errors must leave the remaining feed items for a later run
			if tt.want && !isRetriableLater(tt.err) {
				t.Errorf("isRetriableLater(%v) = false, want true", tt.err)
			}
		})
	}

	if !isRetriableLater(ErrNoAvailableAPIKey) {
		t.Error("isRetriableLater(ErrNoAvailableAPIKey) = false, want true")
	}
	if !isRetriableLater(genai.APIError{Code: 503, Message: "The model is overloaded. Please try again later."}) {
		t.Error("isRetriableLater(503 overloaded) = false, want true")
	}
	if isRetriableLater(errors.New("boom")) {
		t.Error("isRetriableLater(boom) = true, want false")
	}
}

// a 429 that `gt.IsQuotaExceeded` does not recognize must still cool the combo
// down and fail over, instead of failing the whole run
func TestWithFailoverFailsOverOnUnrecognized429(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // 2 combos

	now := time.Unix(1_000_000, 0)
	nowFn := func() time.Time { return now }

	calls := 0
	_, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		calls++
		if calls < 2 {
			return genai.APIError{Code: 429, Message: "Resource has been exhausted (e.g. check quota)."}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after failover, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 failover), got %d", calls)
	}
	if len(c.cooldownUntil) != 1 {
		t.Errorf("expected 1 cooled-down combo, got %d", len(c.cooldownUntil))
	}
}

func TestFailedSummaryIncludesModel(t *testing.T) {
	err := errors.New("boom 429")

	withModel := failedSummary("gemini-3-flash-preview", err)
	if !strings.HasPrefix(withModel, ErrorPrefixSummaryFailedWithError) {
		t.Errorf("missing error prefix: %q", withModel)
	}
	if !strings.Contains(withModel, "[gemini-3-flash-preview]") {
		t.Errorf("expected model in message, got %q", withModel)
	}

	noModel := failedSummary("", err)
	if !strings.HasPrefix(noModel, ErrorPrefixSummaryFailedWithError) {
		t.Errorf("missing error prefix: %q", noModel)
	}
	if strings.Contains(noModel, "[") {
		t.Errorf("expected no model bracket when model empty, got %q", noModel)
	}
}

// cooldowns must survive a restart when a db cache is used, or the escalation
// of (b) has no effect on single-shot (eg. cron'ed) runs
func TestCooldownsArePersisted(t *testing.T) {
	dbPath := fmt.Sprintf("%s/cooldowns.db", t.TempDir())

	quotaErr := genai.APIError{
		Code:    429,
		Message: "exceeded your current quota",
		Details: []map[string]any{
			{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "600s"},
		},
	}
	now := time.Now()

	first, err := NewClientWithDB([]string{"key-a", "key-b"}, nil, dbPath)
	if err != nil {
		t.Fatalf("failed to create client with DB: %s", err)
	}
	first.SetGoogleAIModels([]string{"m1"}) // idx0=(key-a,m1), idx1=(key-b,m1)
	first.markCooldown(0, quotaErr, now)
	first.markCooldown(0, quotaErr, now) // 2 consecutive failures

	// a newly created client on the same db must see it
	second, err := NewClientWithDB([]string{"key-a", "key-b"}, nil, dbPath)
	if err != nil {
		t.Fatalf("failed to re-create client with DB: %s", err)
	}
	second.SetGoogleAIModels([]string{"m1"})

	if got, exists := second.cooldownUntil[0]; !exists || !got.Equal(first.cooldownUntil[0]) {
		t.Errorf("cooldown of idx0 = %v (exists: %v), want %v", got, exists, first.cooldownUntil[0])
	}
	if got := second.cooldownFailures[0]; got != 2 {
		t.Errorf("failure count of idx0 = %d, want 2", got)
	}
	if _, exists := second.cooldownUntil[1]; exists {
		t.Errorf("idx1 was never cooled down, but has a cooldown")
	}

	// the api keys themselves must not be persisted
	for _, cooldown := range second.cache.LoadCooldowns() {
		if strings.Contains(cooldown.APIKeyHash, "key-a") || strings.Contains(cooldown.APIKeyHash, "key-b") {
			t.Errorf("api key persisted as-is: %q", cooldown.APIKeyHash)
		}
	}

	// a successful call clears the persisted cooldown too
	if _, err := second.withFailover(func() time.Time { return first.cooldownUntil[0].Add(time.Second) },
		func(gtc *gt.Client, model string) error { return nil }); err != nil {
		t.Fatalf("expected success, got %s", err)
	}
	third, err := NewClientWithDB([]string{"key-a", "key-b"}, nil, dbPath)
	if err != nil {
		t.Fatalf("failed to re-create client with DB: %s", err)
	}
	third.SetGoogleAIModels([]string{"m1"})
	if len(third.cooldownUntil) != 0 {
		t.Errorf("expected the persisted cooldown to be cleared, got %+v", third.cooldownUntil)
	}
}

// a cooldown which expired long ago must not be restored, so that its failure
// count does not keep escalating forever
func TestStaleCooldownsAreNotRestored(t *testing.T) {
	dbPath := fmt.Sprintf("%s/stale.db", t.TempDir())

	c, err := NewClientWithDB([]string{"key-a"}, nil, dbPath)
	if err != nil {
		t.Fatalf("failed to create client with DB: %s", err)
	}
	c.SetGoogleAIModels([]string{"m1"})

	if err := c.cache.SaveCooldown(CachedCooldown{
		APIKeyHash: hashAPIKey("key-a"),
		Model:      "m1",
		Until:      time.Now().Add(-2 * staleCooldownSeconds * time.Second),
		Failures:   9,
	}); err != nil {
		t.Fatalf("failed to save cooldown: %s", err)
	}

	restarted, err := NewClientWithDB([]string{"key-a"}, nil, dbPath)
	if err != nil {
		t.Fatalf("failed to re-create client with DB: %s", err)
	}
	restarted.SetGoogleAIModels([]string{"m1"})

	if len(restarted.cooldownUntil) != 0 || len(restarted.cooldownFailures) != 0 {
		t.Errorf("stale cooldown was restored: until=%+v failures=%+v", restarted.cooldownUntil, restarted.cooldownFailures)
	}
}

// exhausting every combo must not hide *why*: the returned error has to carry
// the last quota error (which quota, and its retry delay) along with the
// ErrNoAvailableAPIKey sentinel
func TestWithFailoverKeepsQuotaErrorDetails(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1"}) // 2 combos

	now := time.Now()
	nowFn := func() time.Time { return now }

	quotaErr := genai.APIError{
		Code:    429,
		Message: "You exceeded your current quota",
		Details: []map[string]any{
			{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "42s"},
			{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{
				map[string]any{"quotaId": "GenerateRequestsPerDayPerProjectPerModel"},
			}},
		},
	}

	_, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		return quotaErr
	})

	if !errors.Is(err, ErrNoAvailableAPIKey) {
		t.Errorf("expected ErrNoAvailableAPIKey in the chain, got %v", err)
	}
	if !isQuotaError(err) {
		t.Errorf("expected the quota error to stay unwrappable, got %v", err)
	}
	if !isRetriableLater(err) {
		t.Error("the returned error must be retriable later")
	}
	if got := err.Error(); !strings.Contains(got, "42s") || !strings.Contains(got, "GenerateRequestsPerDayPerProjectPerModel") {
		t.Errorf("expected the quota details in the message, got %q", got)
	}
	// the details must also stay reachable programmatically
	if len(gt.ErrDetails(err)) != 2 {
		t.Errorf("expected 2 error details, got %+v", gt.ErrDetails(err))
	}
}

// with both a 429 and a 503 in one call, the 429 stays the unwrappable one (only
// one genai.APIError is reachable through a chain) and the 503 must at least
// remain visible in the message
func TestWithFailoverKeepsBothQuotaAndOverloadErrors(t *testing.T) {
	c := NewClient([]string{"k1"}, nil)
	c.SetGoogleAIModels([]string{"m1", "m2"}) // 2 combos, 2 models

	now := time.Now()
	nowFn := func() time.Time { return now }

	quotaErr := genai.APIError{Code: 429, Message: "You exceeded your current quota"}
	overloadErr := genai.APIError{Code: 503, Message: "The model is overloaded. Please try again later."}

	calls := 0
	_, err := c.withFailover(nowFn, func(gtc *gt.Client, model string) error {
		calls++
		if calls == 1 {
			return quotaErr
		}
		return overloadErr
	})
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if !errors.Is(err, ErrNoAvailableAPIKey) {
		t.Errorf("expected ErrNoAvailableAPIKey in the chain, got %v", err)
	}
	if !isQuotaError(err) {
		t.Errorf("expected the 429 to stay unwrappable, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "overloaded") {
		t.Errorf("expected the 503 to stay visible in the message, got %q", got)
	}
	if !isRetriableLater(err) {
		t.Error("the returned error must be retriable later")
	}
}

// a 429 naming a daily (RPD) quota must cool the combo down until that quota
// resets, no matter how short a retryDelay the server suggests
//
// NOTE: the details below are an actual free-tier response: a daily quota of 20
// requests, served with 'retryDelay: 34s'
func TestCooldownDurationUntilDailyQuotaReset(t *testing.T) {
	dailyQuotaErr := genai.APIError{
		Code:    429,
		Message: "You exceeded your current quota",
		Status:  "RESOURCE_EXHAUSTED",
		Details: []map[string]any{
			{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{
				map[string]any{
					"quotaId":     "GenerateRequestsPerDayPerProjectPerModel-FreeTier",
					"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests",
					"quotaValue":  "20",
				},
			}},
			{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "34s"},
		},
	}

	loc, err := time.LoadLocation(dailyQuotaResetTimezone)
	if err != nil {
		t.Skipf("timezone %s unavailable: %s", dailyQuotaResetTimezone, err)
	}

	for _, now := range []time.Time{
		time.Date(2026, 8, 26, 9, 30, 0, 0, loc),      // morning, ~14.5h to reset
		time.Date(2026, 8, 26, 23, 40, 0, 0, loc),     // just before the reset
		time.Date(2026, 8, 26, 0, 10, 0, 0, loc),      // just after a reset
		time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), // a caller in another timezone
	} {
		got := cooldownDuration(dailyQuotaErr, 1, now)

		reset, resolved := nextDailyQuotaReset(now)
		if !resolved {
			t.Fatal("expected the daily reset to resolve")
		}
		if !reset.After(now) {
			t.Errorf("now = %s: reset %s is not in the future", now, reset)
		}
		if want := reset.Sub(now); got < want {
			t.Errorf("now = %s: cooldown %s ends before the quota resets (%s)", now, got, want)
		}
		// the server's 34s must not be what is used
		if got <= time.Minute {
			t.Errorf("now = %s: cooldown %s looks like the server's retryDelay", now, got)
		}
		// the jitter must stay bounded, even on a ~day-long cooldown
		if want := reset.Sub(now) + maxCooldownJitterSeconds*time.Second; got > want {
			t.Errorf("now = %s: cooldown %s exceeds the reset + max jitter (%s)", now, got, want)
		}
	}

	// a per-minute quota keeps using the server's delay
	minuteQuotaErr := genai.APIError{
		Code:    429,
		Message: "You exceeded your current quota",
		Details: []map[string]any{
			{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{
				map[string]any{"quotaId": "GenerateRequestsPerMinutePerProjectPerModel-FreeTier"},
			}},
			{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "34s"},
		},
	}
	if got := cooldownDuration(minuteQuotaErr, 1, time.Now()); got < 34*time.Second || got > time.Minute {
		t.Errorf("cooldown for a per-minute quota = %s, want ~34s", got)
	}
}

func TestIsDailyQuotaExceeded(t *testing.T) {
	quotaFailure := func(quotaID string) []map[string]any {
		return []map[string]any{
			{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{
				map[string]any{"quotaId": quotaID},
			}},
		}
	}

	tests := []struct {
		name    string
		details []map[string]any
		want    bool
	}{
		{name: "daily", details: quotaFailure("GenerateRequestsPerDayPerProjectPerModel-FreeTier"), want: true},
		{name: "per minute", details: quotaFailure("GenerateRequestsPerMinutePerProjectPerModel-FreeTier")},
		{name: "no quota failure", details: []map[string]any{{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "1s"}}},
		{name: "malformed violations", details: []map[string]any{{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": "nope"}}},
		{name: "nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDailyQuotaExceeded(tt.details); got != tt.want {
				t.Errorf("isDailyQuotaExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}
