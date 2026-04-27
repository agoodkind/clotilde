package adapter

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/config"
)

func TestHandleChatLogsSummaryBody(t *testing.T) {
	t.Parallel()
	srv, buf := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode:  "summary",
			MaxKB: 32,
		},
	})

	body := map[string]any{
		"model":    "missing-model",
		"messages": []map[string]string{{"role": "system", "content": "ping"}},
	}
	postChatToServer(t, srv, body)

	rawEvt := findRawLogEvent(t, buf)
	if rawEvt == nil {
		t.Fatalf("expected adapter.chat.raw event")
	}
	if rawEvt["msg"] != "adapter.chat.raw" {
		t.Fatalf("msg = %q", rawEvt["msg"])
	}
	if _, hasBody := rawEvt["body"]; hasBody {
		t.Fatalf("summary mode should not include raw body")
	}
	if _, hasSummary := rawEvt["body_summary"]; !hasSummary {
		t.Fatalf("summary mode should include body_summary")
	}
}

func TestHandleChatLogsWhitelistBody(t *testing.T) {
	t.Parallel()
	srv, buf := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode:  "whitelist",
			MaxKB: 4,
		},
	})

	body := map[string]any{
		"model": "missing-model",
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":       "weather",
					"parameters": map[string]any{"city": "string"},
				},
			},
		},
		"tool_choice": "auto",
		"messages": []map[string]any{
			{"role": "user", "content": strings.Repeat("x", 10000)},
		},
	}
	postChatToServer(t, srv, body)

	rawEvt := findRawLogEvent(t, buf)
	if rawEvt == nil {
		t.Fatalf("expected adapter.chat.raw event")
	}
	rawBody, ok := rawEvt["body"].(string)
	if !ok {
		t.Fatalf("body type = %T", rawEvt["body"])
	}
	if len(rawBody) > 4*1024 {
		t.Fatalf("whitelist body should be capped to 4KB")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawBody), &payload); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	tools, ok := payload["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("expected tools in whitelist body")
	}
	tool0, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool shape %T", tools[0])
	}
	if _, hasParams := tool0["parameters"]; hasParams {
		t.Fatalf("tool parameters should be redacted in whitelist body")
	}
	fn, ok := tool0["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool function shape = %T", tool0["function"])
	}
	if fn["name"] != "weather" {
		t.Fatalf("tool name = %v", fn["name"])
	}
	if _, ok := rawEvt["body_summary"]; !ok {
		t.Fatalf("whitelist mode should include body_summary")
	}
}

func TestHandleChatLogsRawBody(t *testing.T) {
	t.Parallel()
	srv, buf := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode:  "raw",
			MaxKB: 1,
		},
	})

	body := map[string]any{
		"model":    "missing-model",
		"messages": []map[string]string{{"role": "system", "content": strings.Repeat("y", 2048)}},
	}
	postChatToServer(t, srv, body)

	rawEvt := findRawLogEvent(t, buf)
	if rawEvt == nil {
		t.Fatalf("expected adapter.chat.raw event")
	}
	rawBody, ok := rawEvt["body"].(string)
	if !ok {
		t.Fatalf("body type = %T", rawEvt["body"])
	}
	if len(rawBody) > 1024 {
		t.Fatalf("raw body should be capped to 1KB")
	}
	if truncated, ok := rawEvt["body_truncated"].(bool); !ok || !truncated {
		t.Fatalf("expected body_truncated=true")
	}
	if _, hasSummary := rawEvt["body_summary"]; !hasSummary {
		t.Fatalf("raw mode should include body_summary")
	}
	bodyB64, ok := rawEvt["body_b64"].(string)
	if !ok {
		t.Fatalf("raw mode should include body_b64")
	}
	decoded, err := base64.StdEncoding.DecodeString(bodyB64)
	if err != nil {
		t.Fatalf("decode body_b64: %v", err)
	}
	var decodedBody map[string]any
	if err := json.Unmarshal(decoded, &decodedBody); err != nil {
		t.Fatalf("unmarshal decoded body_b64: %v", err)
	}
	messages, ok := decodedBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("decoded messages shape = %#v", decodedBody["messages"])
	}
	msg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("decoded message shape = %T", messages[0])
	}
	if got := msg["content"].(string); len(got) != 2048 {
		t.Fatalf("decoded body_b64 content length = %d; want 2048", len(got))
	}
}

func TestHandleChatLogsOffModeSkipsEvent(t *testing.T) {
	t.Parallel()
	srv, buf := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode: "off",
		},
	})
	body := map[string]any{
		"model":    "missing-model",
		"messages": []map[string]string{{"role": "system", "content": "ping"}},
	}
	postChatToServer(t, srv, body)
	if evt := findRawLogEvent(t, buf); evt != nil {
		t.Fatalf("expected no adapter.chat.raw event in off mode")
	}
}

func TestHandleChatAcceptsResponsesInputShape(t *testing.T) {
	t.Parallel()
	srv, _ := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode: "off",
		},
	})

	body := map[string]any{
		"model": "missing-model",
		"input": []map[string]any{
			{"role": "system", "content": "sys"},
			{"role": "user", "content": "hello"},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	srv.mux.ServeHTTP(resp, req)
	if strings.Contains(resp.Body.String(), "messages is required") {
		t.Fatalf("expected input normalization; got body %s", resp.Body.String())
	}
}

func TestHandleChatRejectsUnsupportedBackendWithoutLegacyRunner(t *testing.T) {
	t.Parallel()
	srv, _ := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode: "off",
		},
	})

	payload, err := json.Marshal(map[string]any{
		"model": "clyde-haiku-4-5",
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	srv.mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var out ErrorResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal error response: %v body=%s", err, resp.Body.String())
	}
	if out.Error.Type != "unsupported_backend" {
		t.Fatalf("error type = %q body=%s", out.Error.Type, resp.Body.String())
	}
}

func TestHandleChatLogsCursorModelNormalization(t *testing.T) {
	t.Parallel()
	srv, buf := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode: "off",
		},
	}, func(cfg *config.AdapterConfig) {
		cfg.Codex.Enabled = true
		cfg.Codex.NativeModelRouting = "codex"
	})

	body := map[string]any{
		"model": "gpt-5.4",
		"reasoning": map[string]any{
			"effort":  "high",
			"summary": "auto",
		},
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "Subagent",
				},
			},
		},
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	}
	postChatToServer(t, srv, body)

	evt := findLogEvent(t, buf, "adapter.chat.received")
	if evt == nil {
		t.Fatalf("expected adapter.chat.received event")
	}
	if evt["alias"] != "gpt-5.4" {
		t.Fatalf("alias=%v want gpt-5.4", evt["alias"])
	}
	if evt["cursor_raw_model"] != "gpt-5.4" {
		t.Fatalf("cursor_raw_model=%v", evt["cursor_raw_model"])
	}
	if evt["cursor_normalized_model"] != "gpt-5.4" {
		t.Fatalf("cursor_normalized_model=%v", evt["cursor_normalized_model"])
	}
	if evt["cursor_request_path"] != "foreground" {
		t.Fatalf("cursor_request_path=%v", evt["cursor_request_path"])
	}
	if evt["cursor_can_spawn_agent"] != true {
		t.Fatalf("cursor_can_spawn_agent=%v", evt["cursor_can_spawn_agent"])
	}
}

func TestHandleChatRoutesNativeCodexByDefaultWhenCodexEnabled(t *testing.T) {
	t.Parallel()
	srv, buf := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode: "off",
		},
	}, func(cfg *config.AdapterConfig) {
		cfg.Codex.Enabled = true
		cfg.Codex.ModelPrefixes = []string{"gpt-", "o"}
	})

	body := map[string]any{
		"model": "gpt-5.4",
		"reasoning": map[string]any{
			"effort": "medium",
		},
		"input": []map[string]any{
			{"role": "user", "content": "ping"},
		},
	}
	postChatToServer(t, srv, body)

	if evt := findLogEvent(t, buf, "adapter.model.resolve_failed"); evt != nil {
		t.Fatalf("did not expect model resolution failure: %v", evt)
	}
	evt := findLogEvent(t, buf, "adapter.chat.received")
	if evt == nil {
		t.Fatalf("expected adapter.chat.received event")
	}
	if evt["alias"] != "gpt-5.4" {
		t.Fatalf("alias=%v want gpt-5.4", evt["alias"])
	}
	if evt["backend"] != BackendCodex {
		t.Fatalf("backend=%v want %s", evt["backend"], BackendCodex)
	}
}

func TestHandleChatModelResolutionErrorUsesCursorNativeShape(t *testing.T) {
	t.Parallel()
	srv, buf := newLoggingServer(t, config.LoggingConfig{
		Body: config.LoggingBody{
			Mode: "off",
		},
	}, func(cfg *config.AdapterConfig) {
		cfg.Codex.Enabled = true
		cfg.Codex.NativeModelRouting = "off"
	})

	payload, err := json.Marshal(map[string]any{
		"model": "gpt-5.4",
		"input": []map[string]any{
			{"role": "user", "content": "ping"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	srv.mux.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", resp.Code, resp.Body.String())
	}
	var out ErrorResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal error response: %v body=%s", err, resp.Body.String())
	}
	if out.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %q body=%s", out.Error.Type, resp.Body.String())
	}
	if out.Error.Code != "model_not_found" {
		t.Fatalf("error code = %q body=%s", out.Error.Code, resp.Body.String())
	}
	if out.Error.Param != "model" {
		t.Fatalf("error param = %q body=%s", out.Error.Param, resp.Body.String())
	}

	evt := findLogEvent(t, buf, "adapter.model.resolve_failed")
	if evt == nil {
		t.Fatalf("expected adapter.model.resolve_failed event")
	}
	if evt["model"] != "gpt-5.4" {
		t.Fatalf("model=%v want gpt-5.4", evt["model"])
	}
	if evt["cursor_normalized_model"] != "gpt-5.4" {
		t.Fatalf("cursor_normalized_model=%v", evt["cursor_normalized_model"])
	}
}

func newLoggingServer(t *testing.T, logging config.LoggingConfig, opts ...func(*config.AdapterConfig)) (*Server, *bytes.Buffer) {
	t.Helper()
	cfg := baseConfig()
	cfg.Fallback = config.AdapterFallback{Enabled: false}
	for _, opt := range opts {
		opt(&cfg)
	}

	logBuffer := &bytes.Buffer{}
	srv, err := New(cfg, logging, Deps{
		ScratchDir: func() string { return t.TempDir() },
	}, slog.New(slog.NewJSONHandler(logBuffer, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	logBuffer.Reset()
	return srv, logBuffer
}

func postChatToServer(t *testing.T, srv *Server, body map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	srv.mux.ServeHTTP(resp, req)
}

func findRawLogEvent(t *testing.T, logBuffer *bytes.Buffer) map[string]any {
	return findLogEvent(t, logBuffer, "adapter.chat.raw")
}

func findLogEvent(t *testing.T, logBuffer *bytes.Buffer, message string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logBuffer.String()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt["msg"] == message {
			return evt
		}
	}
	return nil
}
