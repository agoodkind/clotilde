package codex

import (
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

	"github.com/gorilla/websocket"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type ResponseCreateClientMetadata map[string]string

type ResponseCreateWsRequest struct {
	Type               string                       `json:"type"`
	Model              string                       `json:"model,omitempty"`
	Instructions       string                       `json:"instructions,omitempty"`
	Input              []map[string]any             `json:"input,omitempty"`
	Tools              []any                        `json:"tools,omitempty"`
	ToolChoice         string                       `json:"tool_choice,omitempty"`
	ParallelToolCalls  bool                         `json:"parallel_tool_calls,omitempty"`
	Reasoning          *Reasoning                   `json:"reasoning,omitempty"`
	Store              bool                         `json:"store"`
	Stream             bool                         `json:"stream"`
	Include            []string                     `json:"include,omitempty"`
	ServiceTier        string                       `json:"service_tier,omitempty"`
	PromptCacheKey     string                       `json:"prompt_cache_key,omitempty"`
	Text               any                          `json:"text,omitempty"`
	ClientMetadata     ResponseCreateClientMetadata `json:"client_metadata,omitempty"`
	PreviousResponseID string                       `json:"previous_response_id,omitempty"`
	Generate           *bool                        `json:"generate,omitempty"`
}

var ErrWebsocketFallbackToHTTP = errors.New("codex websocket fallback to http")

const defaultWebsocketPrewarmTimeout = 1500 * time.Millisecond

func ResponseCreateRequestFromHTTP(req HTTPTransportRequest) ResponseCreateWsRequest {
	return ResponseCreateWsRequest{
		Type:              "response.create",
		Model:             req.Model,
		Instructions:      req.Instructions,
		Input:             req.Input,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Reasoning:         req.Reasoning,
		Store:             req.Store,
		Stream:            req.Stream,
		Include:           req.Include,
		ServiceTier:       req.ServiceTier,
		PromptCacheKey:    req.PromptCache,
		Text:              req.Text,
		ClientMetadata:    ResponseCreateClientMetadata(req.ClientMetadata),
	}
}

func WithWarmupGenerateFalse(req ResponseCreateWsRequest) ResponseCreateWsRequest {
	generate := false
	req.Generate = &generate
	return req
}

func WithPreviousResponseID(req ResponseCreateWsRequest, previousResponseID string, incrementalInput []map[string]any) ResponseCreateWsRequest {
	req.PreviousResponseID = previousResponseID
	if incrementalInput != nil {
		req.Input = incrementalInput
	}
	return req
}

func MarshalResponseCreateWsRequest(req ResponseCreateWsRequest) ([]byte, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// Force `input: []` whenever the request semantically carries an
	// empty input but the field would otherwise be omitted by
	// json:omitempty. The upstream rejects the frame with
	// "Missing required parameter: 'input'" when the field is
	// absent. Cases:
	//   - Warmup (Generate == false): always sends empty input.
	//   - Continuation (PreviousResponseID set, no new items): the
	//     prior response's items are server-side; we send no delta.
	isWarmup := req.Generate != nil && !*req.Generate
	forceEmptyInput := req.Input != nil && len(req.Input) == 0 &&
		(isWarmup || req.PreviousResponseID != "")
	forceEmptyTools := isWarmup && req.Tools != nil && len(req.Tools) == 0
	if !forceEmptyInput && !forceEmptyTools {
		return raw, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if forceEmptyInput {
		payload["input"] = []map[string]any{}
	}
	if forceEmptyTools {
		payload["tools"] = []any{}
	}
	return json.Marshal(payload)
}

type WebsocketTransportConfig struct {
	URL            string
	Token          string
	AccountID      string
	RequestID      string
	Alias          string
	ConversationID string
	TurnState      *TurnState
	Prewarm        bool
	PrewarmTimeout time.Duration
	BodyLog        BodyLogConfig

	// SessionCache enables persistent ws session reuse when set. The
	// transport takes the cached session for ConversationID, sends a
	// delta payload referencing the cached LastResponseID, then puts
	// the session back on success. When nil or ConversationID is
	// empty, the transport falls back to the legacy fresh-dial path
	// that warms up and closes per call.
	SessionCache *WebsocketSessionCache
	// Log carries ws_session telemetry events. Optional; falls back
	// to slog.Default().
	Log *slog.Logger
}

// Mirrors the observed Responses websocket envelope from
// research/codex/scripts/mock_responses_websocket_server.py.
type websocketEventEnvelope struct {
	Type  string                 `json:"type"`
	Error *websocketErrorPayload `json:"error,omitempty"`
}

type websocketErrorPayload struct {
	Message string `json:"message,omitempty"`
}

func websocketMessageToSyntheticSSE(message []byte) ([]byte, error) {
	var raw websocketEventEnvelope
	if err := json.Unmarshal(message, &raw); err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(raw.Type)
	if kind == "" {
		return nil, fmt.Errorf("codex websocket message missing type")
	}
	if kind == "error" {
		msg := "codex websocket error"
		if raw.Error != nil && strings.TrimSpace(raw.Error.Message) != "" {
			msg = raw.Error.Message
		}
		return nil, fmt.Errorf("%s", msg)
	}
	var b bytes.Buffer
	_, _ = fmt.Fprintf(&b, "event: %s\n", kind)
	_, _ = fmt.Fprintf(&b, "data: %s\n\n", bytes.TrimSpace(message))
	return b.Bytes(), nil
}

func streamWebsocketAsSyntheticSSE(conn *websocket.Conn) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for {
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if messageType != websocket.TextMessage {
				continue
			}
			frame, err := websocketMessageToSyntheticSSE(message)
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write(frame); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			var raw websocketEventEnvelope
			if err := json.Unmarshal(message, &raw); err == nil {
				if raw.Type == "response.completed" || raw.Type == "response.failed" {
					return
				}
			}
		}
	}()
	return pr
}

func writeAndParseWebsocketRequest(
	conn *websocket.Conn,
	cfg WebsocketTransportConfig,
	payload ResponseCreateWsRequest,
	emit func(adapteropenai.StreamChunk) error,
	warmup bool,
) (RunResult, error) {
	raw, err := MarshalResponseCreateWsRequest(payload)
	if err != nil {
		return NewRunResult("stop"), err
	}
	logWebsocketFrame(context.Background(), cfg, payload, raw, warmup)
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		return NewRunResult("stop"), err
	}
	synthetic := streamWebsocketAsSyntheticSSE(conn)
	result, err := ParseTransportStream(synthetic, cfg.RequestID, cfg.Alias, nil, emit)
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return result, nil
	}
	return result, err
}

// logWebsocketFrame emits codex.responses.request for every websocket
// frame Clyde writes (warmup and primary). The frame bytes are exactly
// what the wire receives, so corruption between BuildRequest and the
// websocket write is observable in the JSONL feed.
func logWebsocketFrame(ctx context.Context, cfg WebsocketTransportConfig, payload ResponseCreateWsRequest, frame []byte, warmup bool) {
	mode, maxBytes := cfg.BodyLog.Resolve()
	if mode == BodyLogOff {
		return
	}
	ev := requestEvent{
		Subcomponent:       "codex",
		Transport:          "responses_websocket",
		RequestID:          cfg.RequestID,
		Alias:              cfg.Alias,
		Model:              payload.Model,
		URL:                cfg.URL,
		BodyBytes:          len(frame),
		InputCount:         len(payload.Input),
		ToolCount:          len(payload.Tools),
		PreviousResponseID: payload.PreviousResponseID,
		Warmup:             warmup,
	}
	body, b64, truncated := applyBodyMode(frame, mode, maxBytes)
	ev.Body = body
	ev.BodyB64 = b64
	ev.BodyTruncated = truncated
	if mode == BodyLogSummary || mode == BodyLogWhitelist {
		ev.BodySummary = summarizeWsRequest(payload)
	}
	logCodexEvent(ctx, slog.LevelDebug, "codex.responses.request", ev.toSlogAttrs())
}

func dialResponsesWebsocket(ctx context.Context, cfg WebsocketTransportConfig) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{}
	installationID, _ := LoadInstallationID()
	turnMetadataJSON := ""
	conv := strings.TrimSpace(cfg.ConversationID)
	if conv != "" {
		if json, err := NewTurnMetadata(conv, "").MarshalCompact(); err == nil {
			turnMetadataJSON = json
		}
	}
	header := BuildResponsesWebsocketHeaders(ResponsesWebsocketHeaderConfig{
		RequestID:      cfg.RequestID,
		ConversationID: cfg.ConversationID,
		Token:          cfg.Token,
		InstallationID: installationID,
		TurnState:      cfg.TurnState,
		TurnMetadata:   turnMetadataJSON,
	})
	conn, resp, err := dialer.DialContext(ctx, cfg.URL, header)
	if resp != nil && cfg.TurnState != nil {
		cfg.TurnState.CaptureFromHeaders(resp.Header)
	}
	return conn, resp, err
}

func logWebsocketPrepared(ctx context.Context, cfg WebsocketTransportConfig, payload ResponseCreateWsRequest, telemetry TransportTelemetry) {
	telemetry.RequestID = cfg.RequestID
	telemetry.Alias = cfg.Alias
	telemetry.UpstreamModel = payload.Model
	telemetry.Transport = "responses_websocket"
	telemetry.ServiceTier = payload.ServiceTier
	telemetry.PromptCacheKey = payload.PromptCacheKey
	telemetry.ClientMetadata = map[string]string(payload.ClientMetadata)
	telemetry.InputCount = len(payload.Input)
	telemetry.ToolCount = len(payload.Tools)
	telemetry.PreviousResponseID = payload.PreviousResponseID
	telemetry.TurnStatePresent = cfg.TurnState.Value() != ""
	LogTransportPrepared(ctx, nil, telemetry)
}

func RunWebsocketTransport(
	ctx context.Context,
	cfg WebsocketTransportConfig,
	payload ResponseCreateWsRequest,
	emit func(adapteropenai.StreamChunk) error,
) (RunResult, error) {
	if cfg.SessionCache != nil && strings.TrimSpace(cfg.ConversationID) != "" {
		return runWebsocketWithCache(ctx, cfg, payload, emit)
	}
	return runWebsocketFreshDial(ctx, cfg, payload, emit)
}

// runWebsocketFreshDial is the legacy path. Dial a fresh websocket,
// optionally warm up, send one frame, close. Preserved so tests and
// non-cache callers do not break. Tagged for removal once all
// callers route through runWebsocketWithCache.
func runWebsocketFreshDial(
	ctx context.Context,
	cfg WebsocketTransportConfig,
	payload ResponseCreateWsRequest,
	emit func(adapteropenai.StreamChunk) error,
) (RunResult, error) {
	conn, resp, err := dialResponsesWebsocket(ctx, cfg)
	if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
		logWebsocketPrepared(ctx, cfg, payload, TransportTelemetry{FallbackToHTTP: true})
		return NewRunResult("stop"), ErrWebsocketFallbackToHTTP
	}
	if err != nil {
		return NewRunResult("stop"), err
	}
	defer conn.Close()

	prewarmUsed := false
	prewarmFailed := false
	connectionReused := false
	if cfg.Prewarm && strings.TrimSpace(payload.PreviousResponseID) == "" {
		warmup := WithWarmupGenerateFalse(payload)
		warmup.Tools = []any{}
		logWebsocketPrepared(ctx, cfg, warmup, TransportTelemetry{WebsocketWarmup: true})
		prewarmTimeout := cfg.PrewarmTimeout
		if prewarmTimeout <= 0 {
			prewarmTimeout = defaultWebsocketPrewarmTimeout
		}
		_ = conn.SetReadDeadline(time.Now().Add(prewarmTimeout))
		warmupResult, warmupErr := writeAndParseWebsocketRequest(conn, cfg, warmup, func(adapteropenai.StreamChunk) error {
			return nil
		}, true)
		_ = conn.SetReadDeadline(time.Time{})
		if warmupErr == nil && strings.TrimSpace(warmupResult.ResponseID) != "" {
			payload = WithPreviousResponseID(payload, warmupResult.ResponseID, []map[string]any{})
			prewarmUsed = true
			connectionReused = true
		} else {
			prewarmFailed = true
			_ = conn.Close()
			conn, resp, err = dialResponsesWebsocket(ctx, cfg)
			if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
				logWebsocketPrepared(ctx, cfg, payload, TransportTelemetry{
					FallbackToHTTP:         true,
					WebsocketPrewarmFailed: prewarmFailed,
				})
				return NewRunResult("stop"), ErrWebsocketFallbackToHTTP
			}
			if err != nil {
				return NewRunResult("stop"), err
			}
			defer conn.Close()
		}
	}

	logWebsocketPrepared(ctx, cfg, payload, TransportTelemetry{
		WebsocketPrewarmUsed:     prewarmUsed,
		WebsocketPrewarmFailed:   prewarmFailed,
		WebsocketConnectionReuse: connectionReused,
	})

	return writeAndParseWebsocketRequest(conn, cfg, payload, emit, false)
}

// runWebsocketWithCache implements the parity-superset path. The
// transport takes a cached session keyed on ConversationID, computes
// a delta of the input items relative to the prior baseline, sets
// previous_response_id from the cache entry, sends one frame, and
// returns the session to the cache on success. On any error the
// session is invalidated. Reference: codex-rs/core/src/client.rs
// stream_responses().
func runWebsocketWithCache(
	ctx context.Context,
	cfg WebsocketTransportConfig,
	payload ResponseCreateWsRequest,
	emit func(adapteropenai.StreamChunk) error,
) (RunResult, error) {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	conv := strings.TrimSpace(cfg.ConversationID)
	fullInput := payload.Input

	session, hit := cfg.SessionCache.Take(conv)
	if hit {
		log.InfoContext(ctx, "adapter.codex.ws_session.taken",
			"component", "adapter",
			"subcomponent", "codex",
			"conversation_id", conv,
			"last_response_id", session.LastResponseID,
			"frame_count", session.FrameCount,
			"age_ms", time.Since(session.OpenedAt).Milliseconds(),
		)
	}
	if hit {
		delta := ComputeDelta(session.LastInputItems, fullInput)
		switch {
		case delta.Ok:
			payload = WithPreviousResponseID(payload, session.LastResponseID, delta.Items)
		case delta.Reason == "no_extension":
			cfg.SessionCache.Invalidate(conv, "no_extension")
			session = nil
			hit = false
		default:
			cfg.SessionCache.Invalidate(conv, delta.Reason)
			session = nil
			hit = false
		}
	}

	if !hit {
		opened, err := openSessionAndWarmup(ctx, cfg, payload, log)
		if err != nil {
			return NewRunResult("stop"), err
		}
		session = opened
		if strings.TrimSpace(session.LastResponseID) != "" {
			payload = WithPreviousResponseID(payload, session.LastResponseID, fullInput)
		}
	}

	logWebsocketPrepared(ctx, cfg, payload, TransportTelemetry{
		WebsocketConnectionReuse: hit,
	})
	log.InfoContext(ctx, "adapter.codex.frame.sent",
		"component", "adapter",
		"subcomponent", "codex",
		"conversation_id", conv,
		"request_id", cfg.RequestID,
		"prev_response_id", payload.PreviousResponseID,
		"delta_input_count", len(payload.Input),
		"full_input_count", len(fullInput),
		"is_warmup", false,
	)

	result, err := writeAndParseWebsocketRequest(session.Conn, cfg, payload, emit, false)
	if err != nil {
		cfg.SessionCache.Invalidate(conv, "ws_io_error")
		return result, err
	}

	session.LastResponseID = strings.TrimSpace(result.ResponseID)
	if session.LastResponseID == "" {
		// Server completed without an id. Drop the connection rather
		// than re-cache without a chain anchor.
		cfg.SessionCache.Invalidate(conv, "missing_response_id")
		return result, nil
	}
	session.LastInputItems = cloneInputItems(fullInput)
	session.FrameCount++
	cfg.SessionCache.Put(session)
	log.InfoContext(ctx, "adapter.codex.ws_session.put",
		"component", "adapter",
		"subcomponent", "codex",
		"conversation_id", conv,
		"last_response_id", session.LastResponseID,
		"frame_count", session.FrameCount,
	)
	return result, nil
}

// openSessionAndWarmup dials a fresh websocket, sends the warmup
// frame (generate=false, empty input, no prev), captures the
// response_id, and returns a populated WebsocketSession ready to
// carry a real frame. The caller is responsible for installing the
// session in the cache after the first real frame succeeds.
func openSessionAndWarmup(
	ctx context.Context,
	cfg WebsocketTransportConfig,
	payload ResponseCreateWsRequest,
	log *slog.Logger,
) (*WebsocketSession, error) {
	conv := strings.TrimSpace(cfg.ConversationID)
	conn, resp, err := dialResponsesWebsocket(ctx, cfg)
	if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
		return nil, ErrWebsocketFallbackToHTTP
	}
	if err != nil {
		return nil, err
	}

	warmup := WithWarmupGenerateFalse(payload)
	warmup.Tools = []any{}
	warmup.Input = []map[string]any{}
	warmup.PreviousResponseID = ""
	prewarmTimeout := cfg.PrewarmTimeout
	if prewarmTimeout <= 0 {
		prewarmTimeout = defaultWebsocketPrewarmTimeout
	}
	_ = conn.SetReadDeadline(time.Now().Add(prewarmTimeout))
	warmupResult, warmupErr := writeAndParseWebsocketRequest(conn, cfg, warmup, func(adapteropenai.StreamChunk) error {
		return nil
	}, true)
	_ = conn.SetReadDeadline(time.Time{})
	if warmupErr != nil || strings.TrimSpace(warmupResult.ResponseID) == "" {
		_ = conn.Close()
		return nil, fmt.Errorf("codex websocket warmup failed: %w", warmupErr)
	}
	now := time.Now()
	session := &WebsocketSession{
		Conn:           conn,
		ConversationID: conv,
		LastResponseID: warmupResult.ResponseID,
		OpenedAt:       now,
		LastUsed:       now,
	}
	if log != nil {
		log.InfoContext(ctx, "adapter.codex.ws_session.opened",
			"component", "adapter",
			"subcomponent", "codex",
			"conversation_id", conv,
			"warmup_response_id", warmupResult.ResponseID,
		)
	}
	return session, nil
}
