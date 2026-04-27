package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
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
	ws.Tools = []any{}
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
	if tools, ok := payload["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("serialized tools=%v want empty array", payload["tools"])
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

	ws = WithPreviousResponseID(base, "resp-123", []map[string]any{})
	encoded, err = MarshalResponseCreateWsRequest(ws)
	if err != nil {
		t.Fatalf("marshal websocket request with empty input: %v", err)
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal empty-input payload: %v", err)
	}
	if input, ok := payload["input"].([]any); !ok || len(input) != 0 {
		t.Fatalf("serialized input=%v want empty array", payload["input"])
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
	}, ResponseCreateWsRequest{Type: "response.create"}, func(adapteropenai.StreamChunk) error {
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
	var chunks []adapteropenai.StreamChunk
	result, err := RunWebsocketTransport(context.Background(), WebsocketTransportConfig{
		URL:            wsURL,
		Token:          "test-token",
		AccountID:      "acct-123",
		RequestID:      "req-ws",
		Alias:          "gpt-5.4",
		ConversationID: "cursor:conv-123",
		TurnState:      turnState,
	}, ResponseCreateWsRequest{Type: "response.create"}, func(ch adapteropenai.StreamChunk) error {
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

func TestRunWebsocketTransportPrewarmsAndReusesConnection(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	var handshakes atomic.Int32
	requestsCh := make(chan []map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handshakes.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()

		var requests []map[string]any
		for idx := 0; idx < 2; idx++ {
			_, requestBody, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read request %d: %v", idx, err)
			}
			var request map[string]any
			if err := json.Unmarshal(requestBody, &request); err != nil {
				t.Fatalf("unmarshal request %d: %v", idx, err)
			}
			requests = append(requests, request)
			events := []map[string]any{
				{"type": "response.created", "response": map[string]any{"id": "warm-1"}},
				{"type": "response.completed", "response": map[string]any{"id": "warm-1", "usage": map[string]any{"input_tokens": 1, "output_tokens": 0, "total_tokens": 1, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}},
			}
			if idx == 1 {
				events = []map[string]any{
					{"type": "response.created", "response": map[string]any{"id": "resp-1"}},
					{"type": "response.output_text.delta", "delta": "done"},
					{"type": "response.completed", "response": map[string]any{"id": "resp-1", "usage": map[string]any{"input_tokens": 10, "output_tokens": 1, "total_tokens": 11, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}},
				}
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
		}
		requestsCh <- requests
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	var chunks []adapteropenai.StreamChunk
	result, err := RunWebsocketTransport(context.Background(), WebsocketTransportConfig{
		URL:            wsURL,
		Token:          "test-token",
		RequestID:      "req-ws",
		Alias:          "gpt-5.4",
		ConversationID: "cursor:conv-123",
		TurnState:      NewTurnState(),
		Prewarm:        true,
	}, ResponseCreateWsRequest{
		Type:  "response.create",
		Model: "gpt-5.4",
		Input: []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
		Tools: []any{map[string]any{"type": "function", "name": "read_file"}},
	}, func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWebsocketTransport: %v", err)
	}
	if result.ResponseID != "resp-1" {
		t.Fatalf("response_id=%q want resp-1", result.ResponseID)
	}
	if handshakes.Load() != 1 {
		t.Fatalf("handshakes=%d want 1", handshakes.Load())
	}
	requests := <-requestsCh
	if len(requests) != 2 {
		t.Fatalf("requests len=%d want 2", len(requests))
	}
	if got, ok := requests[0]["generate"].(bool); !ok || got {
		t.Fatalf("warmup generate=%v want false", requests[0]["generate"])
	}
	if tools, ok := requests[0]["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("warmup tools=%v want empty array", requests[0]["tools"])
	}
	if got, _ := requests[1]["previous_response_id"].(string); got != "warm-1" {
		t.Fatalf("follow-up previous_response_id=%q want warm-1", got)
	}
	if input, ok := requests[1]["input"].([]any); !ok || len(input) != 0 {
		t.Fatalf("follow-up input=%v want empty array", requests[1]["input"])
	}
	var content strings.Builder
	for _, ch := range chunks {
		if len(ch.Choices) > 0 {
			content.WriteString(ch.Choices[0].Delta.Content)
		}
	}
	if got := content.String(); got != "done" {
		t.Fatalf("content=%q want done", got)
	}
}

func TestRunWebsocketTransportReconnectsAfterPrewarmFailure(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	var handshakes atomic.Int32
	requestsCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connNumber := handshakes.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
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
		if connNumber == 1 {
			payload, err := json.Marshal(map[string]any{
				"type":  "response.failed",
				"error": map[string]any{"message": "warmup failed"},
			})
			if err != nil {
				t.Fatalf("marshal failure: %v", err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				t.Fatalf("write failure: %v", err)
			}
			return
		}
		requestsCh <- request
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp-1"}},
			{"type": "response.output_text.delta", "delta": "recovered"},
			{"type": "response.completed", "response": map[string]any{"id": "resp-1", "usage": map[string]any{"input_tokens": 10, "output_tokens": 1, "total_tokens": 11, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}},
		} {
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
	var chunks []adapteropenai.StreamChunk
	result, err := RunWebsocketTransport(context.Background(), WebsocketTransportConfig{
		URL:            wsURL,
		Token:          "test-token",
		RequestID:      "req-ws",
		Alias:          "gpt-5.4",
		ConversationID: "cursor:conv-123",
		TurnState:      NewTurnState(),
		Prewarm:        true,
	}, ResponseCreateWsRequest{
		Type:  "response.create",
		Model: "gpt-5.4",
		Input: []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
	}, func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWebsocketTransport: %v", err)
	}
	if result.ResponseID != "resp-1" {
		t.Fatalf("response_id=%q want resp-1", result.ResponseID)
	}
	if handshakes.Load() != 2 {
		t.Fatalf("handshakes=%d want 2", handshakes.Load())
	}
	request := <-requestsCh
	if _, ok := request["previous_response_id"]; ok {
		t.Fatalf("generated request after failed prewarm should not use previous_response_id: %v", request)
	}
	var content strings.Builder
	for _, ch := range chunks {
		if len(ch.Choices) > 0 {
			content.WriteString(ch.Choices[0].Delta.Content)
		}
	}
	if got := content.String(); got != "recovered" {
		t.Fatalf("content=%q want recovered", got)
	}
}

func TestRunWebsocketTransportTimesOutHungPrewarmAndRunsGeneratedRequest(t *testing.T) {
	t.Parallel()

	upgrader := websocket.Upgrader{}
	var handshakes atomic.Int32
	requestsCh := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connNumber := handshakes.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
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
		if connNumber == 1 {
			if got, ok := request["generate"].(bool); !ok || got {
				t.Fatalf("warmup generate=%v want false", request["generate"])
			}
			time.Sleep(100 * time.Millisecond)
			return
		}
		requestsCh <- request
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp-1"}},
			{"type": "response.output_text.delta", "delta": "after-timeout"},
			{"type": "response.completed", "response": map[string]any{"id": "resp-1", "usage": map[string]any{"input_tokens": 10, "output_tokens": 1, "total_tokens": 11, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}},
		} {
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
	var chunks []adapteropenai.StreamChunk
	result, err := RunWebsocketTransport(context.Background(), WebsocketTransportConfig{
		URL:            wsURL,
		Token:          "test-token",
		RequestID:      "req-ws",
		Alias:          "gpt-5.4",
		ConversationID: "cursor:conv-123",
		TurnState:      NewTurnState(),
		Prewarm:        true,
		PrewarmTimeout: 20 * time.Millisecond,
	}, ResponseCreateWsRequest{
		Type:  "response.create",
		Model: "gpt-5.4",
		Input: []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
	}, func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("RunWebsocketTransport: %v", err)
	}
	if result.ResponseID != "resp-1" {
		t.Fatalf("response_id=%q want resp-1", result.ResponseID)
	}
	if handshakes.Load() != 2 {
		t.Fatalf("handshakes=%d want 2", handshakes.Load())
	}
	request := <-requestsCh
	if _, ok := request["previous_response_id"]; ok {
		t.Fatalf("generated request after timed-out prewarm should not use previous_response_id: %v", request)
	}
	var content strings.Builder
	for _, ch := range chunks {
		if len(ch.Choices) > 0 {
			content.WriteString(ch.Choices[0].Delta.Content)
		}
	}
	if got := content.String(); got != "after-timeout" {
		t.Fatalf("content=%q want after-timeout", got)
	}
}

func TestCodexTransportParityMatrixSerialization(t *testing.T) {
	t.Parallel()

	maxCompletion := 3072
	httpReq := HTTPTransportRequest{
		Model:                "gpt-5.4",
		Instructions:         "base instructions",
		Input:                []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
		Tools:                []any{map[string]any{"type": "function", "name": "read_file"}},
		ToolChoice:           "auto",
		ParallelToolCalls:    true,
		Reasoning:            &Reasoning{Effort: "medium"},
		Include:              []string{"reasoning.encrypted_content"},
		ServiceTier:          "priority",
		PromptCache:          "cursor:conv-123",
		PromptCacheRetention: "24h",
		Text:                 json.RawMessage(`{"verbosity":"high"}`),
		Truncation:           "auto",
		MaxCompletion:        &maxCompletion,
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
	if got, _ := httpPayload["prompt_cache_retention"].(string); got != "24h" {
		t.Fatalf("http prompt_cache_retention=%q want 24h", got)
	}
	if got, _ := httpPayload["truncation"].(string); got != "auto" {
		t.Fatalf("http truncation=%q want auto", got)
	}
	text, _ := httpPayload["text"].(map[string]any)
	if text["verbosity"] != "high" {
		t.Fatalf("http text=%v want verbosity high", httpPayload["text"])
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
	if got, _ := wsPayload["prompt_cache_retention"].(string); got != "24h" {
		t.Fatalf("ws prompt_cache_retention=%q want 24h", got)
	}
	if got, _ := wsPayload["truncation"].(string); got != "auto" {
		t.Fatalf("ws truncation=%q want auto", got)
	}
	text, _ = wsPayload["text"].(map[string]any)
	if text["verbosity"] != "high" {
		t.Fatalf("ws text=%v want verbosity high", wsPayload["text"])
	}
	if got, _ := wsPayload["previous_response_id"].(string); got != "resp-123" {
		t.Fatalf("ws previous_response_id=%q want resp-123", got)
	}
	if got, ok := wsPayload["generate"].(bool); !ok || got {
		t.Fatalf("ws generate=%v want false", wsPayload["generate"])
	}
}
