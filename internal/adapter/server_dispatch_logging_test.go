package adapter

import (
	"bytes"
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

func newLoggingServer(t *testing.T, logging config.LoggingConfig) (*Server, *bytes.Buffer) {
	t.Helper()
	cfg := baseConfig()
	cfg.Fallback = config.AdapterFallback{Enabled: false}

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
		if evt["msg"] == "adapter.chat.raw" {
			return evt
		}
	}
	return nil
}
