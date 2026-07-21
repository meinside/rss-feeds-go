package rf

import (
	"errors"
	"strings"
	"testing"
	"time"

	gt "github.com/meinside/gemini-things-go"
)

func TestBuildCombos(t *testing.T) {
	c := NewClient([]string{"k1", "k2"}, nil)
	c.SetGoogleAIModels([]string{"m1", "m2", "m3"})

	if len(c.combos) != 6 {
		t.Fatalf("expected 6 combos, got %d", len(c.combos))
	}

	// combos are ordered (k1,m1) first ... (k2,m3) last (key outer loop, model inner loop)
	if c.combos[0] != (keyModelCombo{apiKey: "k1", model: "m1"}) {
		t.Errorf("combos[0] = %+v", c.combos[0])
	}
	if c.combos[5] != (keyModelCombo{apiKey: "k2", model: "m3"}) {
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
		combo, _, ok := c.pickAvailableCombo(now)
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
		combo, idx, ok := c.pickAvailableCombo(now)
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

	if _, _, ok := c.pickAvailableCombo(now); ok {
		t.Errorf("expected ok=false when all combos in cooldown")
	}

	// available again after cooldown expires
	later := now.Add(31 * time.Second)
	if _, _, ok := c.pickAvailableCombo(later); !ok {
		t.Errorf("expected ok=true after cooldown expired")
	}
}

func TestParseRetryDelay(t *testing.T) {
	tests := []struct {
		name    string
		details []map[string]any
		want    time.Duration
	}{
		{
			name: "valid retry info",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "37s"},
			},
			want: 37 * time.Second,
		},
		{
			name: "fractional seconds",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "1.5s"},
			},
			want: 1500 * time.Millisecond,
		},
		{
			name:    "nil details -> fallback",
			details: nil,
			want:    defaultCooldownSeconds * time.Second,
		},
		{
			name: "no retry info -> fallback",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.QuotaFailure"},
			},
			want: defaultCooldownSeconds * time.Second,
		},
		{
			name: "unparsable delay -> fallback",
			details: []map[string]any{
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "soon"},
			},
			want: defaultCooldownSeconds * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRetryDelay(tt.details); got != tt.want {
				t.Errorf("parseRetryDelay() = %v, want %v", got, tt.want)
			}
		})
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
