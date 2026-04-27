package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

// HTTPTransportRequest is the current root-owned Codex request wire shape.
// This stays in the Codex package so the direct HTTP SSE transport can be
// exercised and moved independently of the root adapter facade.
type HTTPTransportRequest struct {
	Model                string            `json:"model"`
	Instructions         string            `json:"instructions"`
	Store                bool              `json:"store"`
	Stream               bool              `json:"stream"`
	Include              []string          `json:"include,omitempty"`
	PromptCache          string            `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string            `json:"prompt_cache_retention,omitempty"`
	ServiceTier          string            `json:"service_tier,omitempty"`
	Text                 json.RawMessage   `json:"text,omitempty"`
	Truncation           string            `json:"truncation,omitempty"`
	ClientMetadata       map[string]string `json:"client_metadata,omitempty"`
	Reasoning            *Reasoning        `json:"reasoning,omitempty"`
	MaxCompletion        *int              `json:"max_completion_tokens,omitempty"`
	Input                []map[string]any  `json:"input"`
	Tools                []any             `json:"tools,omitempty"`
	ToolChoice           string            `json:"tool_choice,omitempty"`
	ParallelToolCalls    bool              `json:"parallel_tool_calls,omitempty"`
}

type HTTPTransportConfig struct {
	BaseURL        string
	Token          string
	AccountID      string
	RequestID      string
	Alias          string
	ConversationID string
	BodyLog        BodyLogConfig
}

func RunHTTPTransport(
	ctx context.Context,
	httpClient *http.Client,
	cfg HTTPTransportConfig,
	payload HTTPTransportRequest,
	emit func(adapteropenai.StreamChunk) error,
) (RunResult, error) {
	conversationID := strings.TrimSpace(payload.PromptCache)
	windowID := ""
	if conversationID != "" {
		windowID = CodexWindowID(conversationID)
		payload.ClientMetadata = ClientMetadata(cfg.AccountID, windowID)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return NewRunResult("stop"), err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL, bytes.NewReader(raw))
	if err != nil {
		return NewRunResult("stop"), err
	}
	httpReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if conversationID != "" {
		httpReq.Header.Set("x-client-request-id", conversationID)
		httpReq.Header.Set("session_id", conversationID)
		if windowID != "" {
			httpReq.Header.Set("x-codex-window-id", windowID)
		}
	}

	nativeShellCount, nativeCustomCount, functionToolCount := ToolSpecCounts(payload.Tools)
	LogTransportPrepared(ctx, slog.Default(), TransportTelemetry{
		RequestID:         cfg.RequestID,
		Alias:             cfg.Alias,
		UpstreamModel:     payload.Model,
		Transport:         "responses_http",
		ServiceTier:       payload.ServiceTier,
		MaxCompletion:     payload.MaxCompletion,
		PromptCacheKey:    conversationID,
		ClientMetadata:    payload.ClientMetadata,
		InputCount:        len(payload.Input),
		ToolCount:         len(payload.Tools),
		NativeShellCount:  nativeShellCount,
		NativeCustomCount: nativeCustomCount,
		FunctionToolCount: functionToolCount,
	})

	// codex.responses.request mirrors anthropic.messages.request: it
	// captures outbound wire bytes so payload corruption between
	// BuildRequest and the HTTP write is visible end-to-end. Mode is
	// controlled by logging.body.mode plumbed through DirectConfig.
	mode, maxBytes := cfg.BodyLog.Resolve()
	if mode != BodyLogOff {
		ev := requestEvent{
			Subcomponent: "codex",
			Transport:    "responses_http",
			RequestID:    cfg.RequestID,
			Alias:        cfg.Alias,
			Model:        payload.Model,
			URL:          cfg.BaseURL,
			BodyBytes:    len(raw),
			Headers:      redactedCodexOutboundHeaders(httpReq.Header),
			InputCount:   len(payload.Input),
			ToolCount:    len(payload.Tools),
		}
		body, b64, truncated := applyBodyMode(raw, mode, maxBytes)
		ev.Body = body
		ev.BodyB64 = b64
		ev.BodyTruncated = truncated
		if mode == BodyLogSummary || mode == BodyLogWhitelist {
			ev.BodySummary = summarizeHTTPRequest(payload)
		}
		logCodexEvent(ctx, slog.LevelDebug, "codex.responses.request", ev.toSlogAttrs())
	}

	postStarted := time.Now()
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		logCodexEvent(ctx, slog.LevelError, "codex.responses.post_failed", responseEventCodex{
			Subcomponent: "codex",
			Transport:    "responses_http",
			RequestID:    cfg.RequestID,
			Alias:        cfg.Alias,
			Model:        payload.Model,
			BodyBytes:    len(raw),
			DurationMs:   time.Since(postStarted).Milliseconds(),
			Err:          err.Error(),
		}.toSlogAttrs())
		return NewRunResult("stop"), err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		errBody, errB64, errTruncated := applyBodyMode(b, mode, maxBytes)
		logCodexEvent(ctx, slog.LevelError, "codex.responses.upstream_error", responseEventCodex{
			Subcomponent:  "codex",
			Transport:     "responses_http",
			RequestID:     cfg.RequestID,
			Alias:         cfg.Alias,
			Model:         payload.Model,
			Status:        resp.StatusCode,
			BodyBytes:     len(b),
			DurationMs:    time.Since(postStarted).Milliseconds(),
			Body:          errBody,
			BodyB64:       errB64,
			BodyTruncated: errTruncated,
		}.toSlogAttrs())
		return NewRunResult("stop"), fmt.Errorf("codex backend %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	logCodexEvent(ctx, slog.LevelInfo, "codex.responses.connected", responseEventCodex{
		Subcomponent: "codex",
		Transport:    "responses_http",
		RequestID:    cfg.RequestID,
		Alias:        cfg.Alias,
		Model:        payload.Model,
		Status:       resp.StatusCode,
		BodyBytes:    len(raw),
		DurationMs:   time.Since(postStarted).Milliseconds(),
	}.toSlogAttrs())
	return ParseTransportStream(resp.Body, cfg.RequestID, cfg.Alias, slog.Default(), emit)
}
