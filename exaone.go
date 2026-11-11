package rf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// EXAONE API Platform (https://docs.exaone.ai), an OpenAI-compatible chat
// completions API.
//
// NOTE: it takes text only, so files, YouTube videos, and url context are left
// to Gemini (see `keyModelCombo.supports`).
const (
	defaultExaoneModel   = "k-exaone_v2.5"
	defaultExaoneBaseURL = "https://api.exaone.ai/v1"

	// the API allows 1 request per second per api key, answering others with
	// 429 'second limit(1), called 1 times per second'
	defaultExaoneRequestInterval = 1 * time.Second

	// backoff before retrying a 5xx without a retry-after header: 5xx ('The
	// service is currently experiencing a temporary issue.') come back fast and
	// often succeed on a retry
	defaultExaoneServerErrorBackoff = 2 * time.Second

	// 429s with a retry-after up to this long are waited out and retried with
	// the same combo, instead of cooling it down: both of the observed limits
	// ('second limit(1)': 1s, 'service limit(600) exceeded per minute': 7s)
	// recover within seconds, and a cooldown would escalate to minutes
	maxExaoneInPlaceRetryAfter = 10 * time.Second

	maxExaoneResponseBytes  = 10 * 1024 * 1024
	maxExaoneErrorBodyRunes = 500
)

// exaoneSettings are the EXAONE settings of a client, overridable in tests.
type exaoneSettings struct {
	baseURL            string
	requestInterval    time.Duration
	serverErrorBackoff time.Duration
}

// defaultExaoneSettings returns the default EXAONE settings.
func defaultExaoneSettings() exaoneSettings {
	return exaoneSettings{
		baseURL:            defaultExaoneBaseURL,
		requestInterval:    defaultExaoneRequestInterval,
		serverErrorBackoff: defaultExaoneServerErrorBackoff,
	}
}

// exaoneAPIError is a non-200 response from the EXAONE API.
type exaoneAPIError struct {
	StatusCode int
	Type       string
	Code       string
	Message    string

	// RetryAfter is from the 'retry-after-ms' (or 'retry-after') header, or 0
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *exaoneAPIError) Error() string {
	kind := e.Code
	if kind == "" {
		kind = e.Type
	}

	msg := fmt.Sprintf("exaone api error (http %d", e.StatusCode)
	if kind != "" {
		msg += ", " + kind
	}
	msg += "): " + e.Message
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return msg
}

// isExaoneServerError reports whether the error is a 5xx from the EXAONE API.
func isExaoneServerError(err error) bool {
	var exaoneErr *exaoneAPIError
	return errors.As(err, &exaoneErr) && exaoneErr.StatusCode >= http.StatusInternalServerError
}

// newExaoneAPIError builds an error from a non-200 response.
//
// The body is either OpenAI-style (`{"error":{"message", "type", "code"}}`,
// observed) or `{"result_code", "description"}` (documented).
func newExaoneAPIError(statusCode int, header http.Header, body []byte) *exaoneAPIError {
	e := &exaoneAPIError{
		StatusCode: statusCode,
		RetryAfter: parseRetryAfterHeader(header),
	}

	var openAIStyle struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"` // string or null
		} `json:"error"`
	}
	var platformStyle struct {
		ResultCode  int    `json:"result_code"`
		Description string `json:"description"`
	}

	switch {
	case json.Unmarshal(body, &openAIStyle) == nil && openAIStyle.Error != nil:
		e.Message = openAIStyle.Error.Message
		e.Type = openAIStyle.Error.Type
		if openAIStyle.Error.Code != nil {
			e.Code = fmt.Sprint(openAIStyle.Error.Code)
		}
	case json.Unmarshal(body, &platformStyle) == nil && platformStyle.Description != "":
		e.Message = platformStyle.Description
		if platformStyle.ResultCode != 0 {
			e.Code = strconv.Itoa(platformStyle.ResultCode)
		}
	default:
		e.Message = truncateRunes(strings.TrimSpace(string(body)), maxExaoneErrorBodyRunes)
	}
	if e.Message == "" {
		e.Message = http.StatusText(statusCode)
	}

	return e
}

// parseRetryAfterHeader returns the delay from 'retry-after-ms', or from
// 'retry-after' (in seconds, or an http date). It returns 0 when absent.
func parseRetryAfterHeader(header http.Header) time.Duration {
	if ms, err := strconv.ParseFloat(header.Get("Retry-After-Ms"), 64); err == nil && ms > 0 {
		return time.Duration(ms * float64(time.Millisecond))
	}
	retryAfter := header.Get("Retry-After")
	if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second))
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

// truncateRunes truncates given string to `n` runes.
func truncateRunes(s string, n int) string {
	if runes := []rune(s); len(runes) > n {
		return string(runes[:n]) + "..."
	}
	return s
}

// request/response bodies of the chat completions API (only the used fields)
type exaoneChatRequest struct {
	Model      string            `json:"model"`
	Messages   []exaoneMessage   `json:"messages"`
	Tools      []exaoneTool      `json:"tools,omitempty"`
	ToolChoice *exaoneToolChoice `json:"tool_choice,omitempty"`
}

type exaoneMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type exaoneTool struct {
	Type     string             `json:"type"`
	Function exaoneToolFunction `json:"function"`
}

type exaoneToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type exaoneToolChoice struct {
	Type     string             `json:"type"`
	Function exaoneToolFunction `json:"function"`
}

type exaoneChatResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			// NOTE: `reasoning_content` is ignored
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"` // JSON string
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

// exaoneTranslateAndSummarizeRequest returns a request which forces the same
// function call as Gemini's (see `genOptionsWithTools`).
//
// NOTE: `max_tokens` is not set, as reasoning tokens count toward it: with a
// small limit, the reasoning alone used it up and the content came back empty.
func exaoneTranslateAndSummarizeRequest(model, prompt string) exaoneChatRequest {
	fn := exaoneToolFunction{
		Name:        fnNameTranslateTitleAndSummarizeContent,
		Description: fnDescTranslateTitleAndSummarizeContent,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				fnParamNameTranslatedTitle: map[string]any{
					"type":        "string",
					"description": fnParamDescTranslatedTitle,
				},
				fnParamNameSummarizedContent: map[string]any{
					"type":        "string",
					"description": fnParamDescSummarizedContent,
				},
			},
			"required": []string{
				fnParamNameTranslatedTitle,
				fnParamNameSummarizedContent,
			},
		},
	}

	return exaoneChatRequest{
		Model: model,
		Messages: []exaoneMessage{
			{Role: "system", Content: systemInstructionForTranslationAndSummary()},
			{Role: "user", Content: prompt},
		},
		Tools: []exaoneTool{{Type: "function", Function: fn}},
		ToolChoice: &exaoneToolChoice{
			Type:     "function",
			Function: exaoneToolFunction{Name: fnNameTranslateTitleAndSummarizeContent},
		},
	}
}

// exaoneTranslateAndSummarize translates and summarizes given text prompt with
// the EXAONE combo.
//
// 5xx, and 429s with a short retry-after, are retried with the same combo up
// to `maxRetryCount` times. When they persist, the error is returned for
// `withFailoverFor` to fail over.
func (c *Client) exaoneTranslateAndSummarize(
	ctx context.Context,
	combo keyModelCombo,
	prompt string,
) (translatedTitle, summarizedContent string, err error) {
	for attempt := 0; ; attempt++ {
		if err = c.waitForExaoneRequestSlot(ctx, combo.apiKeyHash); err != nil {
			return "", "", err
		}

		translatedTitle, summarizedContent, err = c.exaoneGenerate(ctx, combo, prompt)
		if err == nil || attempt >= maxRetryCount {
			return translatedTitle, summarizedContent, err
		}

		wait, retriable := c.exaoneRetryWait(err)
		if !retriable {
			return translatedTitle, summarizedContent, err
		}

		v(c.verbose, "retrying exaone request in %s (retry %d/%d): %s", wait, attempt+1, maxRetryCount, err)

		if serr := sleepContext(ctx, wait); serr != nil {
			return "", "", err
		}
	}
}

// exaoneRetryWait returns how long to wait before retrying the same combo, and
// whether it should be retried at all.
func (c *Client) exaoneRetryWait(err error) (wait time.Duration, retriable bool) {
	var exaoneErr *exaoneAPIError
	if !errors.As(err, &exaoneErr) {
		return 0, false
	}

	switch {
	case exaoneErr.StatusCode == http.StatusTooManyRequests:
		if exaoneErr.RetryAfter > 0 && exaoneErr.RetryAfter <= maxExaoneInPlaceRetryAfter {
			return exaoneErr.RetryAfter, true
		}
		return 0, false // cooled down by `withFailoverFor`
	case exaoneErr.StatusCode >= http.StatusInternalServerError:
		if exaoneErr.RetryAfter > 0 {
			return exaoneErr.RetryAfter, true
		}
		return c.exaone.serverErrorBackoff, true
	}
	return 0, false
}

// waitForExaoneRequestSlot waits until a request with given api key keeps the
// interval from the previous one, and reserves the slot.
func (c *Client) waitForExaoneRequestSlot(ctx context.Context, apiKeyHash string) error {
	c.exaoneMu.Lock()
	now := time.Now()
	at := c.exaoneNextRequestAt[apiKeyHash]
	if at.Before(now) {
		at = now
	}
	if c.exaoneNextRequestAt == nil {
		c.exaoneNextRequestAt = map[string]time.Time{}
	}
	c.exaoneNextRequestAt[apiKeyHash] = at.Add(c.exaone.requestInterval)
	c.exaoneMu.Unlock()

	return sleepContext(ctx, at.Sub(now))
}

// sleepContext sleeps for `d`, or until `ctx` is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// exaoneGenerate sends a single chat completions request, and extracts the
// translated title and summarized content from its response.
func (c *Client) exaoneGenerate(
	ctx context.Context,
	combo keyModelCombo,
	prompt string,
) (translatedTitle, summarizedContent string, err error) {
	body, err := json.Marshal(exaoneTranslateAndSummarizeRequest(combo.model, prompt))
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal exaone request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, generationTimeoutSeconds*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.exaone.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("failed to create exaone request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+combo.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to request exaone api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxExaoneResponseBytes))
	if err != nil {
		return "", "", fmt.Errorf("failed to read exaone response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", newExaoneAPIError(resp.StatusCode, resp.Header, respBody)
	}

	return extractExaoneTranslatedTitleAndSummarizedContent(combo.model, respBody)
}

// extractExaoneTranslatedTitleAndSummarizedContent extracts the translated
// title and summarized content from a chat completions response: from the
// forced function call, or from the content when there is no function call
// (as `geminiTranslateAndSummarize` does).
func extractExaoneTranslatedTitleAndSummarizedContent(model string, body []byte) (translatedTitle, summarizedContent string, err error) {
	var resp exaoneChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", fmt.Errorf("failed to parse exaone response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", "", fmt.Errorf("no choice in exaone response [%s]", model)
	}

	choice := resp.Choices[0]
	if len(choice.Message.ToolCalls) > 0 {
		call := choice.Message.ToolCalls[0]
		if call.Function.Name != fnNameTranslateTitleAndSummarizeContent {
			return "", "", fmt.Errorf("not an expected function name: '%s'", call.Function.Name)
		}

		var args map[string]*string
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", "", fmt.Errorf("could not parse function arguments: %w", err)
		}
		title, content := args[fnParamNameTranslatedTitle], args[fnParamNameSummarizedContent]
		if title == nil {
			return "", "", fmt.Errorf("could not find function argument '%s'", fnParamNameTranslatedTitle)
		}
		if content == nil {
			return "", "", fmt.Errorf("could not find function argument '%s'", fnParamNameSummarizedContent)
		}
		if strings.TrimSpace(*content) == "" {
			return "", "", fmt.Errorf("summarized content was empty [%s]", model)
		}
		return *title, *content, nil
	}

	// FIXME: no function call but text, as sometimes with Gemini
	if content := strings.TrimSpace(choice.Message.Content); content != "" {
		return "", content, nil
	}
	return "", "", fmt.Errorf("summarized content was empty (finish reason: %s) [%s]", choice.FinishReason, model)
}
