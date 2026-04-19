// Client construction and /v1/messages request execution.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/oauth"
)

// New builds a Client. If httpClient is nil a 10 minute timeout
// client is used; long timeouts matter because /v1/messages can keep
// a connection open for the full inference window on large outputs.
// cfg carries the impersonation triplet sourced from
// [adapter.impersonation] in the user's toml. New does not validate
// cfg; callers (the daemon adapter wiring) should refuse to start
// when any field is empty.
func New(httpClient *http.Client, source *oauth.Manager, cfg Config) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Client{http: httpClient, oauth: managerWrap(source), cfg: cfg}
}

// SystemPromptPrefix returns the configured prefix so callers
// (oauth_handler) can prepend it to outgoing system prompts without
// reaching into the Client struct.
func (c *Client) SystemPromptPrefix() string { return c.cfg.SystemPromptPrefix }

// managerWrap lets us pass an *oauth.Manager directly while keeping
// the OAuthSource interface for tests.
func managerWrap(m *oauth.Manager) OAuthSource {
	if m == nil {
		return nil
	}
	return OAuthSource(m)
}

// StreamEvents issues a streaming /v1/messages request and invokes sink
// for each decoded stream event (text, tool-use lifecycle, thinking,
// and final stop).
func (c *Client) StreamEvents(ctx context.Context, req Request, sink EventSink) (Usage, string, error) {
	req.Stream = true
	resp, err := c.do(ctx, req)
	if err != nil {
		return Usage{}, "", err
	}
	defer resp.Body.Close()

	usage := Usage{}
	stopReason := ""
	blockTypes := make(map[int]string)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)

	var currentEvent string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			currentEvent = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(line[len("data:"):])
			if data == "" {
				continue
			}
			if err := dispatchSSE(currentEvent, data, sink, &usage, &stopReason, blockTypes); err != nil {
				return usage, stopReason, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, stopReason, fmt.Errorf("anthropic stream scan: %w", err)
	}
	return usage, stopReason, nil
}

func (c *Client) do(ctx context.Context, req Request) (*http.Response, error) {
	if c.oauth == nil {
		return nil, errors.New("anthropic client missing oauth source")
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = MaxOutputTokens
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	token, err := c.oauth.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("oauth token: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, MessagesURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build messages request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("anthropic-beta", c.cfg.BetaHeader)
	httpReq.Header.Set("anthropic-version", oauth.Version)
	httpReq.Header.Set("x-app", "cli")
	httpReq.Header.Set("User-Agent", c.cfg.UserAgent)
	httpReq.Header.Set("Content-Type", "application/json")

	postStarted := time.Now()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		logResponse(slog.LevelError, "anthropic.messages.post_failed", responseEvent{
			Component:  "anthropic",
			Model:      req.Model,
			BodyBytes:  len(body),
			DurationMs: time.Since(postStarted).Milliseconds(),
			Err:        err.Error(),
		})
		return nil, fmt.Errorf("post /v1/messages: %w", err)
	}

	base := responseEvent{
		Component:  "anthropic",
		Model:      req.Model,
		Status:     resp.StatusCode,
		RequestID:  resp.Header.Get("request-id"),
		BodyBytes:  len(body),
		DurationMs: time.Since(postStarted).Milliseconds(),
		RateLimits: rateLimitAttrs(resp.Header),
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ev := base
		ev.RetryAfter = resp.Header.Get("retry-after")
		ev.Body = truncate(string(errBody), 400)
		logResponse(slog.LevelWarn, "anthropic.ratelimit", ev)
		return nil, fmt.Errorf("anthropic %s: %s", resp.Status, truncate(string(errBody), 600))
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ev := base
		ev.Body = truncate(string(errBody), 400)
		logResponse(slog.LevelError, "anthropic.messages.upstream_error", ev)
		return nil, fmt.Errorf("anthropic %s: %s", resp.Status, truncate(string(errBody), 600))
	}
	logResponse(slog.LevelInfo, "anthropic.messages.connected", base)
	return resp, nil
}
