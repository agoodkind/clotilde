package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gorilla/websocket"
	"goodkind.io/clyde/internal/adapter/tooltrans"
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
	Include            []string                     `json:"include,omitempty"`
	ServiceTier        string                       `json:"service_tier,omitempty"`
	PromptCacheKey     string                       `json:"prompt_cache_key,omitempty"`
	Text               any                          `json:"text,omitempty"`
	ClientMetadata     ResponseCreateClientMetadata `json:"client_metadata,omitempty"`
	PreviousResponseID string                       `json:"previous_response_id,omitempty"`
	Generate           *bool                        `json:"generate,omitempty"`
	MaxCompletion      *int                         `json:"max_completion_tokens,omitempty"`
}

var ErrWebsocketFallbackToHTTP = errors.New("codex websocket fallback to http")

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
		Include:           req.Include,
		ServiceTier:       req.ServiceTier,
		PromptCacheKey:    req.PromptCache,
		ClientMetadata:    ResponseCreateClientMetadata(req.ClientMetadata),
		MaxCompletion:     req.MaxCompletion,
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
	return json.Marshal(req)
}

type WebsocketTransportConfig struct {
	URL            string
	Token          string
	AccountID      string
	RequestID      string
	Alias          string
	ConversationID string
	TurnState      *TurnState
}

func websocketMessageToSyntheticSSE(message []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(message, &raw); err != nil {
		return nil, err
	}
	kind, _ := raw["type"].(string)
	if kind == "" {
		return nil, fmt.Errorf("codex websocket message missing type")
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
			var raw map[string]any
			if err := json.Unmarshal(message, &raw); err == nil {
				if kind, _ := raw["type"].(string); kind == "response.completed" || kind == "response.failed" {
					return
				}
			}
		}
	}()
	return pr
}

func RunWebsocketTransport(
	ctx context.Context,
	cfg WebsocketTransportConfig,
	payload ResponseCreateWsRequest,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (RunResult, error) {
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
	if resp != nil && resp.StatusCode == http.StatusUpgradeRequired {
		LogTransportPrepared(ctx, nil, TransportTelemetry{
			RequestID:        cfg.RequestID,
			Alias:            cfg.Alias,
			UpstreamModel:    payload.Model,
			Transport:        "responses_websocket",
			ServiceTier:      payload.ServiceTier,
			MaxCompletion:    payload.MaxCompletion,
			PromptCacheKey:   payload.PromptCacheKey,
			ClientMetadata:   map[string]string(payload.ClientMetadata),
			InputCount:       len(payload.Input),
			ToolCount:        len(payload.Tools),
			FallbackToHTTP:   true,
			TurnStatePresent: cfg.TurnState.Value() != "",
		})
		return NewRunResult("stop"), ErrWebsocketFallbackToHTTP
	}
	if err != nil {
		return NewRunResult("stop"), err
	}
	defer conn.Close()

	LogTransportPrepared(ctx, nil, TransportTelemetry{
		RequestID:          cfg.RequestID,
		Alias:              cfg.Alias,
		UpstreamModel:      payload.Model,
		Transport:          "responses_websocket",
		ServiceTier:        payload.ServiceTier,
		MaxCompletion:      payload.MaxCompletion,
		PromptCacheKey:     payload.PromptCacheKey,
		ClientMetadata:     map[string]string(payload.ClientMetadata),
		InputCount:         len(payload.Input),
		ToolCount:          len(payload.Tools),
		WebsocketWarmup:    payload.Generate != nil && !*payload.Generate,
		PreviousResponseID: payload.PreviousResponseID,
		TurnStatePresent:   cfg.TurnState.Value() != "",
	})

	raw, err := MarshalResponseCreateWsRequest(payload)
	if err != nil {
		return NewRunResult("stop"), err
	}
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
