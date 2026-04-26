package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestRunDirectFallsBackToHTTPAfterWebsocketUpgradeRequired(t *testing.T) {
	oldGetwd := GetwdFn
	GetwdFn = func() (string, error) { return "/tmp/clyde-test", nil }
	t.Cleanup(func() { GetwdFn = oldGetwd })

	var httpCalled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		httpCalled.Store(true)
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("authorization=%q want bearer token", got)
		}
		var payload HTTPTransportRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Model != "gpt-5.4" {
			t.Fatalf("model=%q want gpt-5.4", payload.Model)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			"event: response.output_text.delta",
			`data: {"delta":"ok"}`,
			"",
			"event: response.completed",
			`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":10}`,
			"",
		}, "\n") + "\n"))
	}))
	defer server.Close()

	var chunks []adapteropenai.StreamChunk
	res, err := RunDirect(context.Background(), DirectConfig{
		HTTPClient:       server.Client(),
		BaseURL:          server.URL + "/responses",
		WebsocketEnabled: true,
		WebsocketURL:     "ws" + strings.TrimPrefix(server.URL, "http") + "/ws",
		Token:            "token-1",
		AccountID:        "acct-1",
		RequestID:        "req-1",
		Continuation:     NewContinuationStore(),
	}, adapteropenai.ChatRequest{
		Messages: []adapteropenai.ChatMessage{{Role: "user", Content: []byte(`"hello"`)}},
	}, adaptermodel.ResolvedModel{
		Alias:       "gpt-5.4",
		ClaudeModel: "gpt-5.4",
	}, "medium", func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("RunDirect: %v", err)
	}
	if !httpCalled.Load() {
		t.Fatalf("expected HTTP fallback to run")
	}
	if res.ResponseID != "resp_1" {
		t.Fatalf("response_id=%q want resp_1", res.ResponseID)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected streamed chunks")
	}
}
