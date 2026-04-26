package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"goodkind.io/clyde/internal/adapter/tooltrans"
)

func TestResponseCreateRequestFromHTTPUsesResponseCreateShape(t *testing.T) {
	maxCompletion := 2048
	req := HTTPTransportRequest{
		Model:             "gpt-5.4",
		Instructions:      "base instructions",
		Input:             []map[string]any{{"type": "message", "role": "user"}},
		Tools:             []any{map[string]any{"type": "function", "name": "read_file"}},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Reasoning:         &Reasoning{Effort: "medium"},
		Include:           []string{"reasoning.encrypted_content"},
		ServiceTier:       "priority",
		PromptCache:       "cursor:conv-123",
		ClientMetadata:    map[string]string{"x-codex-installation-id": "acct-123"},
		MaxCompletion:     &maxCompletion,
	}

	ws := ResponseCreateRequestFromHTTP(req)
	if ws.Type != "response.create" {
		t.Fatalf("type=%q want response.create", ws.Type)
	}
	if ws.ServiceTier != "priority" {
		t.Fatalf("service_tier=%q want priority", ws.ServiceTier)
	}
	if ws.MaxCompletion == nil || *ws.MaxCompletion != maxCompletion {
		t.Fatalf("max_completion_tokens=%v want %d", ws.MaxCompletion, maxCompletion)
	}

	encoded, err := MarshalResponseCreateWsRequest(ws)
	if err != nil {
		t.Fatalf("marshal websocket request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["type"].(string); got != "response.create" {
		t.Fatalf("serialized type=%q want response.create", got)
	}
	if got, _ := payload["service_tier"].(string); got != "priority" {
		t.Fatalf("serialized service_tier=%q want priority", got)
	}
	if got, _ := payload["max_completion_tokens"].(float64); int(got) != maxCompletion {
		t.Fatalf("serialized max_completion_tokens=%v want %d", payload["max_completion_tokens"], maxCompletion)
	}
}

func TestWithWarmupGenerateFalseSetsGenerateFlag(t *testing.T) {
	ws := WithWarmupGenerateFalse(ResponseCreateWsRequest{Type: "response.create"})
	if ws.Generate == nil || *ws.Generate {
		t.Fatalf("generate=%v want false", ws.Generate)
	}
	encoded, err := MarshalResponseCreateWsRequest(ws)
	if err != nil {
		t.Fatalf("marshal websocket request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, ok := payload["generate"].(bool); !ok || got {
		t.Fatalf("serialized generate=%v want false", payload["generate"])
	}
}

func TestWithPreviousResponseIDOverridesInputIncrementally(t *testing.T) {
	base := ResponseCreateWsRequest{
		Type:  "response.create",
		Input: []map[string]any{{"type": "message", "role": "user", "content": "full"}},
	}
	incremental := []map[string]any{{"type": "message", "role": "user", "content": "delta"}}
	ws := WithPreviousResponseID(base, "resp-123", incremental)
	if ws.PreviousResponseID != "resp-123" {
		t.Fatalf("previous_response_id=%q want resp-123", ws.PreviousResponseID)
	}
	if len(ws.Input) != 1 || ws.Input[0]["content"] != "delta" {
		t.Fatalf("input=%v want incremental delta", ws.Input)
	}
	encoded, err := MarshalResponseCreateWsRequest(ws)
	if err != nil {
		t.Fatalf("marshal websocket request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["previous_response_id"].(string); got != "resp-123" {
		t.Fatalf("serialized previous_response_id=%q want resp-123", got)
	}
}

func TestRunWebsocketTransportReturnsFallbackOnUpgradeRequired(t *testing.T) {
	t.Parallel()

	// We do not need a full websocket server here; we only need the
	// handshake status. Point the websocket dial at an HTTP server that
	// returns 426 Upgrade Required so the transport surfaces the same
	// fallback signal codex-rs uses.
	server := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUpgradeRequired)
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	_, err := RunWebsocketTransport(context.Background(), WebsocketTransportConfig{
		URL:       "ws" + strings.TrimPrefix(ts.URL, "http"),
		Token:     "test-token",
		RequestID: "req-1",
		Alias:     "gpt-5.4",
	}, ResponseCreateWsRequest{Type: "response.create"}, func(tooltrans.OpenAIStreamChunk) error {
		return nil
	})
	if !errors.Is(err, ErrWebsocketFallbackToHTTP) {
		t.Fatalf("err=%v want ErrWebsocketFallbackToHTTP", err)
	}
}

func TestRunWebsocketTransportParsesTextAndCompletion(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	turnState := NewTurnState()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-client-request-id"); got != "cursor:conv-123" {
			t.Fatalf("x-client-request-id=%q want cursor:conv-123", got)
		}
		if got := r.Header.Get("session_id"); got != "cursor:conv-123" {
			t.Fatalf("session_id=%q want cursor:conv-123", got)
		}
		if got := r.Header.Get(CodexWindowIDHeader); got != "cursor:conv-123:0" {
			t.Fatalf("%s=%q want cursor:conv-123:0", CodexWindowIDHeader, got)
		}
		if got := r.Header.Get(CodexInstallationIDHeader); got != "acct-123" {
			t.Fatalf("%s=%q want acct-123", CodexInstallationIDHeader, got)
		}
		responseHeader := http.Header{}
		responseHeader.Set(CodexTurnStateHeader, "turn-123")
		conn, err := upgrader.Upgrade(w, r, responseHeader)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()

		_, requestBody, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var request map[string]any
		if err := json.Unmarshal(requestBody, &request); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		if got, _ := request["type"].(string); got != "response.create" {
			t.Fatalf("request type=%q want response.create", got)
		}

		events := []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp-1"}},
			{"type": "response.output_text.delta", "delta": "hello "},
			{"type": "response.output_text.delta", "delta": "world"},
			{"type": "response.completed", "response": map[string]any{"id": "resp-1", "usage": map[string]any{"input_tokens": 10, "output_tokens": 4, "total_tokens": 14, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}},
		}
		for _, event := range events {
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				t.Fatalf("write event: %v", err)
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	var chunks []tooltrans.OpenAIStreamChunk
	result, err := RunWebsocketTransport(context.Background(), WebsocketTransportConfig{
		URL:            wsURL,
		Token:          "test-token",
		AccountID:      "acct-123",
		RequestID:      "req-ws",
		Alias:          "gpt-5.4",
		ConversationID: "cursor:conv-123",
		TurnState:      turnState,
	}, ResponseCreateWsRequest{Type: "response.create"}, func(ch tooltrans.OpenAIStreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWebsocketTransport: %v", err)
	}
	if result.FinishReason != "stop" {
		t.Fatalf("finish_reason=%q want stop", result.FinishReason)
	}
	if result.ResponseID != "resp-1" {
		t.Fatalf("response_id=%q want resp-1", result.ResponseID)
	}
	if got := turnState.Value(); got != "turn-123" {
		t.Fatalf("turn_state=%q want turn-123", got)
	}
	if result.Usage.PromptTokens != 10 || result.Usage.CompletionTokens != 4 || result.Usage.TotalTokens != 14 {
		t.Fatalf("usage=%+v", result.Usage)
	}
	var content strings.Builder
	for _, ch := range chunks {
		if len(ch.Choices) > 0 {
			content.WriteString(ch.Choices[0].Delta.Content)
		}
	}
	if got := content.String(); got != "hello world" {
		t.Fatalf("content=%q want hello world", got)
	}
}

func TestCodexTransportParityMatrixSerialization(t *testing.T) {
	t.Parallel()

	maxCompletion := 3072
	httpReq := HTTPTransportRequest{
		Model:             "gpt-5.4",
		Instructions:      "base instructions",
		Input:             []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
		Tools:             []any{map[string]any{"type": "function", "name": "read_file"}},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Reasoning:         &Reasoning{Effort: "medium"},
		Include:           []string{"reasoning.encrypted_content"},
		ServiceTier:       "priority",
		PromptCache:       "cursor:conv-123",
		MaxCompletion:     &maxCompletion,
	}

	httpEncoded, err := json.Marshal(httpReq)
	if err != nil {
		t.Fatalf("marshal http request: %v", err)
	}
	var httpPayload map[string]any
	if err := json.Unmarshal(httpEncoded, &httpPayload); err != nil {
		t.Fatalf("unmarshal http payload: %v", err)
	}
	if got, _ := httpPayload["model"].(string); got != "gpt-5.4" {
		t.Fatalf("http model=%q want gpt-5.4", got)
	}
	if got, _ := httpPayload["service_tier"].(string); got != "priority" {
		t.Fatalf("http service_tier=%q want priority", got)
	}
	if got, _ := httpPayload["max_completion_tokens"].(float64); int(got) != maxCompletion {
		t.Fatalf("http max_completion_tokens=%v want %d", httpPayload["max_completion_tokens"], maxCompletion)
	}

	wsReq := ResponseCreateRequestFromHTTP(httpReq)
	wsReq = WithWarmupGenerateFalse(wsReq)
	wsReq = WithPreviousResponseID(wsReq, "resp-123", []map[string]any{{"type": "message", "role": "user", "content": "delta"}})
	wsEncoded, err := MarshalResponseCreateWsRequest(wsReq)
	if err != nil {
		t.Fatalf("marshal ws request: %v", err)
	}
	var wsPayload map[string]any
	if err := json.Unmarshal(wsEncoded, &wsPayload); err != nil {
		t.Fatalf("unmarshal ws payload: %v", err)
	}
	if got, _ := wsPayload["type"].(string); got != "response.create" {
		t.Fatalf("ws type=%q want response.create", got)
	}
	if got, _ := wsPayload["service_tier"].(string); got != "priority" {
		t.Fatalf("ws service_tier=%q want priority", got)
	}
	if got, _ := wsPayload["max_completion_tokens"].(float64); int(got) != maxCompletion {
		t.Fatalf("ws max_completion_tokens=%v want %d", wsPayload["max_completion_tokens"], maxCompletion)
	}
	if got, _ := wsPayload["previous_response_id"].(string); got != "resp-123" {
		t.Fatalf("ws previous_response_id=%q want resp-123", got)
	}
	if got, ok := wsPayload["generate"].(bool); !ok || got {
		t.Fatalf("ws generate=%v want false", wsPayload["generate"])
	}
}
