package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type staticToken struct{}

func (staticToken) Token(ctx context.Context) (string, error) {
	return "test-token", nil
}

type rewriteMessagesHost struct {
	serverURL *url.URL
	inner     http.RoundTripper
}

func (t *rewriteMessagesHost) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "REDACTED-UPSTREAM" && strings.HasPrefix(req.URL.Path, "/v1/messages") {
		if t.inner == nil {
			t.inner = http.DefaultTransport
		}
		cloned := req.Clone(req.Context())
		dest := *t.serverURL
		dest.Path = "/v1/messages"
		dest.RawPath = ""
		cloned.URL = &dest
		return t.inner.RoundTrip(cloned)
	}
	if t.inner == nil {
		t.inner = http.DefaultTransport
	}
	return t.inner.RoundTrip(req)
}

func TestMessageMarshalJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "single_text_block_string_content",
			msg: Message{
				Role:    "user",
				Content: []ContentBlock{{Type: "text", Text: "hello"}},
			},
			want: `{"role":"user","content":"hello"}`,
		},
		{
			name: "mixed_blocks_array_content",
			msg: Message{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "see"},
					{Type: "image", Source: &ImageSource{Type: "url", URL: "https://example.com/x.png"}},
				},
			},
			want: `{"role":"user","content":[{"type":"text","text":"see"},{"type":"image","source":{"type":"url","url":"https://example.com/x.png"}}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %s want %s", got, tt.want)
			}
		})
	}
}

func TestMessageMarshalJSONWithCacheControlStaysArray(t *testing.T) {
	t.Parallel()
	msg := Message{
		Role: "user",
		Content: []ContentBlock{{
			Type:         "text",
			Text:         "hello",
			CacheControl: &CacheControl{Type: "ephemeral"},
		}},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}`
	if string(raw) != want {
		t.Fatalf("got %s want %s", raw, want)
	}
}

func TestRequestMarshalToolsAndToolChoice(t *testing.T) {
	t.Parallel()
	req := Request{
		Model:     "claude-3-5-sonnet-20241022",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		MaxTokens: 100,
		Stream:    false,
		Tools: []Tool{
			{
				Name:        "my_tool",
				Description: "does things",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		ToolChoice: &ToolChoice{
			Type:                   "tool",
			Name:                   "my_tool",
			DisableParallelToolUse: true,
		},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	keys := []string{
		`"tools"`,
		`"tool_choice"`,
		`"type":"tool"`,
		`"name":"my_tool"`,
		`"disable_parallel_tool_use":true`,
		`"input_schema"`,
	}
	for _, k := range keys {
		if !strings.Contains(s, k) {
			t.Fatalf("missing substring %q in %s", k, s)
		}
	}
}

// TestStreamEvents_429InvokesOnHeaders asserts that a 429 response fires the
// OnHeaders callback before returning the error, so the chat handler can
// Claim and inject an in-band overage / early-warning notice into the
// user-facing rate-limit error message.
func TestStreamEvents_429InvokesOnHeaders(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
		w.Header().Set("anthropic-ratelimit-unified-overage-status", "allowed")
		w.Header().Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
		w.Header().Set("anthropic-ratelimit-unified-overage-reset", "9999999999")
		w.Header().Set("anthropic-ratelimit-unified-reset", "9999999999")
		w.Header().Set("retry-after", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	t.Cleanup(srv.Close)

	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &rewriteMessagesHost{serverURL: srvURL}}
	cli := &Client{
		http:  hc,
		oauth: &staticToken{},
		cfg: Config{
			MessagesURL:           "https://REDACTED-UPSTREAM/v1/messages",
			OAuthAnthropicVersion: "2023-06-01",
			BetaHeader:            "REDACTED-OAUTH-BETA",
			UserAgent:             "anthropic-test/0",
			CCVersion:             "1.0.0",
			CCEntrypoint:          "test",
		},
	}

	var observed http.Header
	_, _, err = cli.StreamEvents(context.Background(), Request{
		Model:     "claude-test",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "x"}}}},
		MaxTokens: 10,
		OnHeaders: func(h http.Header) { observed = h },
	}, func(StreamEvent) error { return nil })
	if err == nil {
		t.Fatalf("StreamEvents returned nil error on 429; want one")
	}
	if !strings.Contains(err.Error(), "anthropic 429") {
		t.Fatalf("err = %q; want anthropic 429 prefix", err.Error())
	}
	if observed == nil {
		t.Fatalf("OnHeaders was not invoked on 429 response")
	}
	if got := observed.Get("anthropic-ratelimit-unified-status"); got != "rejected" {
		t.Fatalf("OnHeaders received status=%q; want rejected", got)
	}
}

func TestStreamEvents_fixtureSSE(t *testing.T) {
	t.Parallel()
	startPayload, err := json.Marshal(map[string]any{
		"message": map[string]any{
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 0},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cbStart, err := json.Marshal(map[string]any{
		"index": 0,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    "t1",
			"name":  "foo",
			"input": map[string]any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cbDelta, err := json.Marshal(map[string]any{
		"index": 0,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": `{"a":1}`,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cbStop, err := json.Marshal(map[string]any{"index": 0})
	if err != nil {
		t.Fatal(err)
	}
	msgDelta, err := json.Marshal(map[string]any{
		"delta": map[string]any{"stop_reason": "tool_use"},
		"usage": map[string]any{"output_tokens": 5},
	})
	if err != nil {
		t.Fatal(err)
	}

	sse := strings.Join([]string{
		"event: message_start",
		"data: " + string(startPayload),
		"",
		"event: content_block_start",
		"data: " + string(cbStart),
		"",
		"event: content_block_delta",
		"data: " + string(cbDelta),
		"",
		"event: content_block_stop",
		"data: " + string(cbStop),
		"",
		"event: message_delta",
		"data: " + string(msgDelta),
		"",
		"event: message_stop",
		"data: {}",
		"",
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(srv.Close)

	srvURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &rewriteMessagesHost{serverURL: srvURL}}
	cli := &Client{
		http:  hc,
		oauth: &staticToken{},
		cfg: Config{
			MessagesURL:             "https://REDACTED-UPSTREAM/v1/messages",
			OAuthAnthropicVersion:   "2023-06-01",
			BetaHeader:              "REDACTED-OAUTH-BETA",
			UserAgent:               "anthropic-test/0",
			SystemPromptPrefix:      "",
			StainlessPackageVersion: "0",
			StainlessRuntime:        "node",
			StainlessRuntimeVersion: "v0",
			CCVersion:               "1.0.0",
			CCEntrypoint:            "test",
		},
	}

	var got []StreamEvent
	usage, stop, err := cli.StreamEvents(context.Background(), Request{
		Model:     "claude-test",
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "x"}}}},
		MaxTokens: 10,
	}, func(ev StreamEvent) error {
		got = append(got, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamEvents: %v", err)
	}
	if stop != "tool_use" {
		t.Fatalf("stop reason: got %q want tool_use", stop)
	}
	if usage.InputTokens != 1 || usage.OutputTokens != 5 {
		t.Fatalf("usage: got %+v want input 1 output 5", usage)
	}

	want := []StreamEvent{
		{Kind: "tool_use_start", BlockIndex: 0, ToolUseID: "t1", ToolUseName: "foo"},
		{Kind: "tool_use_arg_delta", BlockIndex: 0, PartialJSON: `{"a":1}`},
		{Kind: "tool_use_stop", BlockIndex: 0},
		{Kind: "stop", StopReason: "tool_use"},
	}
	if len(got) != len(want) {
		t.Fatalf("len got %d want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d: got %#v want %#v", i, got[i], want[i])
		}
	}
}
