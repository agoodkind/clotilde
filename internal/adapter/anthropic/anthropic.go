// Package anthropic is a minimal /v1/messages client tailored to the
// clyde adapter. It speaks the Anthropic native API directly (not the
// OpenAI-shaped surface) so the adapter can authenticate with the
// user's Claude.ai OAuth bearer token and bill against their
// subscription instead of going through the metered API key path.
//
// The client translates OpenAI-style chat requests into Messages calls,
// parses streamed SSE events, and surfaces text deltas, tool-use
// lifecycle hints, and final usage to the caller. Prompt caching stays
// friendly for the common single text block via string-shaped JSON
// content when marshaling a lone text block.
//
// Wire format references (from claude-code-2.1.88 sourcemap):
//   - POST https://REDACTED-UPSTREAM/v1/messages
//   - headers: Authorization: Bearer <oauth>, anthropic-beta:
//     REDACTED-OAUTH-BETA, anthropic-version: 2023-06-01,
//     x-app: cli, content-type: application/json
//   - body: { model, system, messages, max_tokens, stream, thinking? }
//   - SSE event types relayed back: message_start, content_block_start,
//     content_block_delta, content_block_stop, message_delta, message_stop
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goodkind.io/clyde/internal/adapter/oauth"
)

// MessagesURL is the Anthropic Messages endpoint.
const MessagesURL = "https://REDACTED-UPSTREAM/v1/messages"

// MaxOutputTokens is the upper bound the adapter requests when the
// caller doesn't pin one. Anthropic requires max_tokens; OpenAI's
// API defaults to "model max" which we approximate generously.
const MaxOutputTokens = 8192

// Config carries the Claude Code identity signals the client mirrors
// on every /v1/messages call. There are no compiled-in defaults;
// values come from [adapter.impersonation] in the user's toml. See
// the AdapterImpersonation doc on the config package for the drift
// profile of each field.
type Config struct {
	// BetaHeader is the comma-joined value of the anthropic-beta
	// header. At minimum it must contain REDACTED-OAUTH-BETA (for OAuth
	// bearer auth). REDACTED-CC-BETA routes into the Claude Code
	// OAuth bucket; effort-2025-11-24 unlocks output_config.effort.
	BetaHeader string
	// UserAgent is the value of the User-Agent header. Pinned to a
	// real CLI version string so the request matches the upstream
	// CLI's bucket.
	UserAgent string
	// SystemPromptPrefix is prepended to every outgoing system
	// prompt. /v1/messages discriminates the OAuth bucket on system
	// content too; caller-supplied system text is preserved after
	// the prefix.
	SystemPromptPrefix string
}

// Client wraps an http.Client and an oauth.Manager.
type Client struct {
	http  *http.Client
	oauth OAuthSource
	cfg   Config
}

// OAuthSource is the minimum surface anthropic.Client needs from
// the oauth manager. Defined as a small interface so callers can
// swap it for a fake in tests.
type OAuthSource interface {
	Token(ctx context.Context) (string, error)
}

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

// ContentBlock is one element in a message content array or a streamed
// content block start payload subset we reuse for decoding.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Source    *ImageSource    `json:"source,omitempty"`
}

// ImageSource describes image bytes or a URL for image content blocks.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// Message is one entry in the /v1/messages "messages" array.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// MarshalJSON emits a string "content" when there is exactly one text
// block; otherwise it emits a JSON array of blocks. Both shapes are
// accepted by Anthropic; the string form preserves prompt-cache
// behavior for the common plain-text path.
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Content) == 1 && m.Content[0].Type == "text" {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			Role:    m.Role,
			Content: m.Content[0].Text,
		})
	}
	return json.Marshal(struct {
		Role    string         `json:"role"`
		Content []ContentBlock `json:"content"`
	}{
		Role:    m.Role,
		Content: m.Content,
	})
}

// Tool is the wire shape for the tools array on /v1/messages.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolChoice selects how the model may use tools.
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// Request is the subset of the /v1/messages body we generate.
type Request struct {
	Model        string        `json:"model"`
	System       string        `json:"system,omitempty"`
	Messages     []Message     `json:"messages"`
	MaxTokens    int           `json:"max_tokens"`
	Stream       bool          `json:"stream"`
	OutputConfig *OutputConfig `json:"output_config,omitempty"`
	// Thinking selects extended thinking. Three shapes are accepted
	// by the API: {type:"adaptive"} (no budget), {type:"enabled",
	// budget_tokens:N}, and {type:"disabled"}. Adaptive thinking is
	// rejected on haiku-4-5 with
	// `400 adaptive thinking is not supported on this model`;
	// enabled requires `max_tokens > budget_tokens`.
	Thinking   *Thinking   `json:"thinking,omitempty"`
	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`
}

// OutputConfig is the wire shape that wraps effort and (later) other
// per-request output knobs. Claude Code's BetaOutputConfig nests
// effort under this object; sending it top-level returns
// `400 effort: Extra inputs are not permitted` even with the
// effort-2025-11-24 beta header active. Verified empirically
// 2026-04-18.
type OutputConfig struct {
	// Effort is one of low/medium/high/max. The adapter only sets
	// this when the registry says the family supports it; haiku-4-5
	// returns `400 This model does not support the effort parameter`.
	Effort string `json:"effort,omitempty"`
}

// Thinking is the optional extended-thinking knob. BudgetTokens uses
// `omitempty` so the adaptive shape doesn't serialize a stray
// `"budget_tokens":0` (which the API rejects).
type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// Usage mirrors the prompt/completion token counts from the response.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Sink receives a single text delta chunk during streaming. Empty
// chunks are skipped.
type Sink func(delta string) error

// StreamEvent is a decoded streaming signal from /v1/messages SSE.
type StreamEvent struct {
	Kind        string
	Text        string
	BlockIndex  int
	ToolUseID   string
	ToolUseName string
	PartialJSON string
	StopReason  string
}

// EventSink receives structured stream events.
type EventSink func(StreamEvent) error

// Result is the aggregated outcome of a non-streaming call.
type Result struct {
	Text  string
	Usage Usage
	Stop  string
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

// rateLimitAttr is one anthropic-ratelimit-* response header captured
// alongside a /v1/messages response. Kept as a typed pair so the
// response event struct stays free of []any until the very last
// moment when slog needs the variadic shape.
type rateLimitAttr struct {
	Name  string
	Value string
}

// rateLimitAttrs extracts every anthropic-ratelimit-* response header
// as typed pairs. These are how Anthropic surfaces remaining quota
// and reset windows; capture them on every response so we can
// correlate 429s with the bucket state that produced them.
func rateLimitAttrs(h http.Header) []rateLimitAttr {
	attrs := make([]rateLimitAttr, 0, 8)
	for key, values := range h {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, "anthropic-ratelimit-") {
			continue
		}
		if len(values) == 0 {
			continue
		}
		attrs = append(attrs, rateLimitAttr{Name: lower, Value: values[0]})
	}
	return attrs
}

// dedicated file logger ---------------------------------------------
// Mirror every anthropic-package event to a separate JSONL file so
// rate-limit / impersonation diagnostics live in one place even if
// the global slog handler is reconfigured. The default slog logger
// still receives the same event.

var (
	fileLoggerOnce sync.Once
	fileLogger     *slog.Logger
)

// AnthropicLogPath returns the JSONL file the anthropic package
// double-writes its events to. Honors $CLYDE_ANTHROPIC_LOG_PATH for
// tests; otherwise lives next to the unified clyde log under
// $XDG_STATE_HOME/clyde/anthropic.jsonl.
func AnthropicLogPath() string {
	if p := os.Getenv("CLYDE_ANTHROPIC_LOG_PATH"); p != "" {
		return p
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "clyde", "anthropic.jsonl")
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "clyde", "anthropic.jsonl")
}

func dedicatedLogger() *slog.Logger {
	fileLoggerOnce.Do(func() {
		path := AnthropicLogPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		fileLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	return fileLogger
}

// responseEvent is the typed payload for every /v1/messages response
// log line (success, ratelimit, upstream error, post failure). The
// only place that materializes []any for slog is toSlogAttrs(); call
// sites build a struct literal and hand it to logResponse, which
// keeps the variadic shape contained to a single helper.
//
// Optional fields use the zero value as the "omit" sentinel:
// RetryAfter/Body/Err empty strings are dropped, and Status==0 means
// the response never came back (post_failed).
type responseEvent struct {
	Component  string
	Model      string
	Status     int
	RequestID  string
	BodyBytes  int
	DurationMs int64
	RateLimits []rateLimitAttr
	RetryAfter string
	Body       string
	Err        string
}

// toSlogAttrs flattens the struct into the variadic any-pair shape
// slog.Logger.Log demands. Optional fields are omitted when zero so
// log lines stay narrow on success and wide only when there's
// something to report.
func (e responseEvent) toSlogAttrs() []any {
	attrs := make([]any, 0, 14+2*len(e.RateLimits))
	if e.Component != "" {
		attrs = append(attrs, "component", e.Component)
	}
	if e.Model != "" {
		attrs = append(attrs, "model", e.Model)
	}
	if e.Status != 0 {
		attrs = append(attrs, "status", e.Status)
	}
	if e.RequestID != "" {
		attrs = append(attrs, "request_id", e.RequestID)
	}
	attrs = append(attrs, "body_bytes", e.BodyBytes)
	attrs = append(attrs, "duration_ms", e.DurationMs)
	for _, r := range e.RateLimits {
		attrs = append(attrs, r.Name, r.Value)
	}
	if e.RetryAfter != "" {
		attrs = append(attrs, "retry_after", e.RetryAfter)
	}
	if e.Body != "" {
		attrs = append(attrs, "body", e.Body)
	}
	if e.Err != "" {
		attrs = append(attrs, "err", e.Err)
	}
	return attrs
}

// logResponse writes the event to both slog.Default() and the
// dedicated anthropic JSONL file. The dedicated file is best effort;
// a missing log dir never blocks API traffic.
func logResponse(level slog.Level, event string, e responseEvent) {
	attrs := e.toSlogAttrs()
	slog.Default().Log(context.Background(), level, event, attrs...)
	if l := dedicatedLogger(); l != nil {
		l.Log(context.Background(), level, event, attrs...)
	}
}

// SSE event payload types. Each struct mirrors exactly one event
// name from /v1/messages streaming (message_start, content_block_delta,
// message_delta, error). Lifting them out of dispatchSSE keeps the
// JSON wire shape inspectable in one place and avoids the rule
// against inline anonymous structs.

// streamMessageUsage is the usage object inside a message_start event.
type streamMessageUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// streamMessage is the message object inside a message_start event.
type streamMessage struct {
	Usage streamMessageUsage `json:"usage"`
}

// streamMessageStartEvent is the full payload for `event: message_start`.
type streamMessageStartEvent struct {
	Message streamMessage `json:"message"`
}

// streamContentBlockSpec is the content_block object on content_block_start.
type streamContentBlockSpec struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// streamContentBlockStartEvent is the full payload for
// `event: content_block_start`.
type streamContentBlockStartEvent struct {
	Index        int                    `json:"index"`
	ContentBlock streamContentBlockSpec `json:"content_block"`
}

// streamContentBlockStopEvent is the full payload for
// `event: content_block_stop`.
type streamContentBlockStopEvent struct {
	Index int `json:"index"`
}

// streamContentBlockDeltaPayload is the delta object inside a
// content_block_delta event.
type streamContentBlockDeltaPayload struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
	Thinking    string `json:"thinking"`
}

// streamContentBlockDeltaEvent is the full payload for
// `event: content_block_delta`.
type streamContentBlockDeltaEvent struct {
	Index int                            `json:"index"`
	Delta streamContentBlockDeltaPayload `json:"delta"`
}

// streamMessageDeltaPayload is the delta object inside a
// message_delta event (carries stop_reason).
type streamMessageDeltaPayload struct {
	StopReason string `json:"stop_reason"`
}

// streamMessageDeltaUsage is the usage delta on a message_delta event
// (only output_tokens is updated mid-stream).
type streamMessageDeltaUsage struct {
	OutputTokens int `json:"output_tokens"`
}

// streamMessageDeltaEvent is the full payload for `event: message_delta`.
type streamMessageDeltaEvent struct {
	Delta streamMessageDeltaPayload `json:"delta"`
	Usage streamMessageDeltaUsage   `json:"usage"`
}

// streamErrorPayload is the error object inside an error event.
type streamErrorPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// streamErrorEvent is the full payload for `event: error`.
type streamErrorEvent struct {
	Error streamErrorPayload `json:"error"`
}

// dispatchSSE decodes one SSE data payload according to the
// currentEvent name and forwards structured events / usage / stop reasons.
func dispatchSSE(
	eventName, data string,
	sink EventSink,
	usage *Usage,
	stop *string,
	blockTypes map[int]string,
) error {
	switch eventName {
	case "ping":
		return nil
	case "message_start":
		var ev streamMessageStartEvent
		if err := json.Unmarshal([]byte(data), &ev); err == nil {
			usage.InputTokens = ev.Message.Usage.InputTokens
			usage.OutputTokens = ev.Message.Usage.OutputTokens
		}
	case "content_block_start":
		var ev streamContentBlockStartEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		t := ev.ContentBlock.Type
		blockTypes[ev.Index] = t
		switch t {
		case "tool_use":
			return sink(StreamEvent{
				Kind:        "tool_use_start",
				BlockIndex:  ev.Index,
				ToolUseID:   ev.ContentBlock.ID,
				ToolUseName: ev.ContentBlock.Name,
			})
		case "thinking":
			return sink(StreamEvent{
				Kind:       "thinking",
				BlockIndex: ev.Index,
			})
		}
	case "content_block_delta":
		var ev streamContentBlockDeltaEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text == "" {
				return nil
			}
			return sink(StreamEvent{
				Kind:       "text",
				Text:       ev.Delta.Text,
				BlockIndex: ev.Index,
			})
		case "input_json_delta":
			return sink(StreamEvent{
				Kind:        "tool_use_arg_delta",
				BlockIndex:  ev.Index,
				PartialJSON: ev.Delta.PartialJSON,
			})
		case "thinking_delta":
			return sink(StreamEvent{
				Kind:       "thinking",
				Text:       ev.Delta.Thinking,
				BlockIndex: ev.Index,
			})
		}
	case "content_block_stop":
		var ev streamContentBlockStopEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if blockTypes[ev.Index] == "tool_use" {
			delete(blockTypes, ev.Index)
			return sink(StreamEvent{
				Kind:       "tool_use_stop",
				BlockIndex: ev.Index,
			})
		}
		delete(blockTypes, ev.Index)
	case "message_delta":
		var ev streamMessageDeltaEvent
		if err := json.Unmarshal([]byte(data), &ev); err == nil {
			if ev.Delta.StopReason != "" {
				*stop = ev.Delta.StopReason
			}
			if ev.Usage.OutputTokens > 0 {
				usage.OutputTokens = ev.Usage.OutputTokens
			}
		}
	case "message_stop":
		return sink(StreamEvent{
			Kind:       "stop",
			StopReason: *stop,
		})
	case "error":
		var ev streamErrorEvent
		if err := json.Unmarshal([]byte(data), &ev); err == nil {
			return fmt.Errorf("anthropic error: %s: %s", ev.Error.Type, ev.Error.Message)
		}
		return fmt.Errorf("anthropic error: %s", truncate(data, 400))
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
