package compact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"goodkind.io/gklog"
)

// CountTokensEndpoint is the Anthropic Messages count_tokens endpoint.
// Override only in tests via NewTokenCounter.
const CountTokensEndpoint = "https://api.anthropic.com/v1/messages/count_tokens"

// AnthropicVersion is the API version header sent with every request.
// Bumped only when Anthropic deprecates the older value.
const AnthropicVersion = "2023-06-01"

// TokenCounter calls Anthropic's POST /v1/messages/count_tokens with a
// single user message whose content is the synthesized content array.
// No Haiku fallback: failures propagate so the orchestrator stops
// rather than silently using an estimate.
type TokenCounter struct {
	APIKey   string
	Endpoint string
	Model    string
	Client   *http.Client
}

// NewTokenCounter builds a counter that posts to the production
// endpoint with a 60s timeout. apiKey is required and never logged.
func NewTokenCounter(apiKey, model string) *TokenCounter {
	return &TokenCounter{
		APIKey:   apiKey,
		Endpoint: CountTokensEndpoint,
		Model:    model,
		Client:   &http.Client{Timeout: 60 * time.Second},
	}
}

// CountResponse is the parsed response payload. Anthropic returns
// only input_tokens for count_tokens.
type CountResponse struct {
	InputTokens int `json:"input_tokens"`
}

// maxRateLimitRetries caps how many times CountSyntheticUser retries a
// 429 response. At the 100 req/min default, the planner's target loop
// easily exceeds the limit on long transcripts; backing off here keeps
// the orchestrator simple.
const maxRateLimitRetries = 6

// CountSyntheticUser counts tokens for a single user message whose
// content is the supplied content-array. Mirrors the exact shape the
// orchestrator will append to the JSONL so the count is honest. On
// HTTP 429 the call honors Retry-After (or falls back to exponential
// backoff) and retries up to maxRateLimitRetries.
func (c *TokenCounter) CountSyntheticUser(ctx context.Context, contentArray []OutputBlock) (int, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "compact", "subcomponent", "count_tokens")
	if c.APIKey == "" {
		log.ErrorContext(ctx, "compact.count_tokens.skipped",
			"reason", "missing_api_key",
		)
		return 0, fmt.Errorf("count_tokens: missing API key")
	}
	if c.Model == "" {
		log.ErrorContext(ctx, "compact.count_tokens.skipped",
			"reason", "missing_model",
		)
		return 0, fmt.Errorf("count_tokens: missing model")
	}
	type countMessage struct {
		Role    string        `json:"role"`
		Content []OutputBlock `json:"content"`
	}
	type countBody struct {
		Model    string         `json:"model"`
		Messages []countMessage `json:"messages"`
	}
	body := countBody{
		Model:    c.Model,
		Messages: []countMessage{{Role: "user", Content: contentArray}},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		log.ErrorContext(ctx, "compact.count_tokens.encode_failed",
			"model", c.Model,
			"err", err,
		)
		return 0, fmt.Errorf("count_tokens: encode body: %w", err)
	}

	var attempt int
	for {
		attempt++
		started := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(encoded))
		if err != nil {
			log.ErrorContext(ctx, "compact.count_tokens.request_build_failed",
				"model", c.Model,
				"err", err,
			)
			return 0, fmt.Errorf("count_tokens: build request: %w", err)
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", c.APIKey)
		req.Header.Set("anthropic-version", AnthropicVersion)

		resp, err := c.Client.Do(req)
		if err != nil {
			log.WarnContext(ctx, "compact.count_tokens.http_failed",
				"model", c.Model,
				"duration_ms", time.Since(started).Milliseconds(),
				"attempt", attempt,
				"err", err,
			)
			return 0, fmt.Errorf("count_tokens: HTTP: %w", err)
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests {
			if attempt >= maxRateLimitRetries {
				log.ErrorContext(ctx, "compact.count_tokens.rate_limited_giving_up",
					"model", c.Model,
					"attempt", attempt,
					"body_excerpt", truncateForError(respBody),
				)
				return 0, fmt.Errorf("count_tokens: status 429 after %d retries: %s", attempt, truncateForError(respBody))
			}
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			if wait == 0 {
				wait = backoffFor(attempt)
			}
			log.WarnContext(ctx, "compact.count_tokens.rate_limited",
				"model", c.Model,
				"attempt", attempt,
				"wait_ms", wait.Milliseconds(),
				"retry_after_header", resp.Header.Get("Retry-After"),
			)
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			log.WarnContext(ctx, "compact.count_tokens.http_bad_status",
				"model", c.Model,
				"status_code", resp.StatusCode,
				"duration_ms", time.Since(started).Milliseconds(),
				"body_excerpt", truncateForError(respBody),
			)
			return 0, fmt.Errorf("count_tokens: status %d: %s", resp.StatusCode, truncateForError(respBody))
		}
		var parsed CountResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			log.WarnContext(ctx, "compact.count_tokens.decode_failed",
				"model", c.Model,
				"duration_ms", time.Since(started).Milliseconds(),
				"err", err,
			)
			return 0, fmt.Errorf("count_tokens: decode response: %w", err)
		}
		if parsed.InputTokens <= 0 {
			log.WarnContext(ctx, "compact.count_tokens.zero_tokens",
				"model", c.Model,
				"duration_ms", time.Since(started).Milliseconds(),
			)
			return 0, fmt.Errorf("count_tokens: zero input_tokens in response")
		}
		log.InfoContext(ctx, "compact.count_tokens.completed",
			"model", c.Model,
			"tokens_in", parsed.InputTokens,
			"duration_ms", time.Since(started).Milliseconds(),
			"attempt", attempt,
		)
		return parsed.InputTokens, nil
	}
}

// parseRetryAfter reads the Retry-After response header and returns a
// duration. Supports both the integer-seconds form and the HTTP-date
// form. Returns zero when the header is absent or unparseable so the
// caller falls back to exponential backoff.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	// Integer seconds is the common shape Anthropic emits.
	var secs int
	if _, err := fmt.Sscanf(h, "%d", &secs); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	// HTTP-date form.
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// backoffFor returns the wait duration when the Retry-After header is
// missing. 500ms, 1s, 2s, 4s, 8s, 15s pattern gives the 429 window
// time to settle on a 100 req/min budget.
func backoffFor(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 500 * time.Millisecond
	case 2:
		return 1 * time.Second
	case 3:
		return 2 * time.Second
	case 4:
		return 4 * time.Second
	case 5:
		return 8 * time.Second
	default:
		return 15 * time.Second
	}
}

// truncateForError keeps API error bodies short so they fit cleanly in
// terminal output without dumping multi-KB stack traces.
func truncateForError(b []byte) string {
	const maxLen = 400
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "..."
}
