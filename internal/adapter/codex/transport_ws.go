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
	forceEmptyInput := req.PreviousResponseID != "" && req.Input != nil && len(req.Input) == 0
	forceEmptyTools := req.Generate != nil && !*req.Generate && req.Tools != nil && len(req.Tools) == 0
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
	header := BuildResponsesWebsocketHeaders(ResponsesWebsocketHeaderConfig{
		RequestID:      cfg.RequestID,
		ConversationID: cfg.ConversationID,
		Token:          cfg.Token,
		InstallationID: cfg.AccountID,
		TurnState:      cfg.TurnState,
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
