package codex

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBodyLogConfigResolveDefaults(t *testing.T) {
	cases := []struct {
		name      string
		cfg       BodyLogConfig
		wantMode  string
		wantBytes int
	}{
		{name: "empty defaults to summary 32k", cfg: BodyLogConfig{}, wantMode: BodyLogSummary, wantBytes: 32 * 1024},
		{name: "raw with 4kb", cfg: BodyLogConfig{Mode: "raw", MaxKB: 4}, wantMode: BodyLogRaw, wantBytes: 4 * 1024},
		{name: "uppercase mode lowercased", cfg: BodyLogConfig{Mode: "WHITELIST", MaxKB: 1}, wantMode: BodyLogWhitelist, wantBytes: 1024},
		{name: "off mode preserved", cfg: BodyLogConfig{Mode: "off", MaxKB: 8}, wantMode: BodyLogOff, wantBytes: 8 * 1024},
		{name: "negative kb falls back to default", cfg: BodyLogConfig{Mode: "raw", MaxKB: -1}, wantMode: BodyLogRaw, wantBytes: 32 * 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, max := tc.cfg.Resolve()
			if mode != tc.wantMode {
				t.Errorf("mode=%q want %q", mode, tc.wantMode)
			}
			if max != tc.wantBytes {
				t.Errorf("max=%d want %d", max, tc.wantBytes)
			}
		})
	}
}

func TestApplyBodyModeOff(t *testing.T) {
	body, b64, truncated := applyBodyMode([]byte(`{"hello":"world"}`), BodyLogOff, 1024)
	if body != "" || b64 != "" || truncated {
		t.Fatalf("off mode should drop body: body=%q b64=%q truncated=%v", body, b64, truncated)
	}
}

func TestApplyBodyModeSummaryDropsBody(t *testing.T) {
	body, b64, truncated := applyBodyMode([]byte(`{"hello":"world"}`), BodyLogSummary, 1024)
	if body != "" || b64 != "" || truncated {
		t.Fatalf("summary mode should not leak body: body=%q b64=%q truncated=%v", body, b64, truncated)
	}
}

func TestApplyBodyModeWhitelistTruncates(t *testing.T) {
	raw := []byte(strings.Repeat("a", 5000))
	body, b64, truncated := applyBodyMode(raw, BodyLogWhitelist, 1024)
	if !truncated {
		t.Fatalf("whitelist should truncate large body")
	}
	if len(body) != 1024 {
		t.Fatalf("body len=%d want 1024", len(body))
	}
	if b64 != "" {
		t.Fatalf("whitelist should not emit base64; got len=%d", len(b64))
	}
}

func TestApplyBodyModeRawIncludesB64(t *testing.T) {
	raw := []byte(`{"k":"v"}`)
	body, b64, truncated := applyBodyMode(raw, BodyLogRaw, 1024)
	if truncated {
		t.Fatalf("small body should not be truncated")
	}
	if body != string(raw) {
		t.Fatalf("body=%q want %q", body, raw)
	}
	if b64 == "" {
		t.Fatalf("raw mode should emit b64")
	}
}

func TestCodexLogPathHonorsOverride(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "codex.jsonl")
	t.Setenv("CLYDE_CODEX_LOG_PATH", tmp)
	if got := CodexLogPath(); got != tmp {
		t.Fatalf("CodexLogPath=%q want %q", got, tmp)
	}
}

func TestCodexLogPathFromXDGState(t *testing.T) {
	t.Setenv("CLYDE_CODEX_LOG_PATH", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-codex-test")
	want := "/tmp/xdg-codex-test/clyde/codex.jsonl"
	if got := CodexLogPath(); got != want {
		t.Fatalf("CodexLogPath=%q want %q", got, want)
	}
}

func TestLogCodexEventDoubleWritesToDedicatedSink(t *testing.T) {
	dir := t.TempDir()
	sinkPath := filepath.Join(dir, "codex.jsonl")
	t.Setenv("CLYDE_CODEX_LOG_PATH", sinkPath)
	codexFileLoggerOnce = sync.Once{}
	codexFileLogger = nil
	t.Cleanup(func() {
		codexFileLoggerOnce = sync.Once{}
		codexFileLogger = nil
	})

	ev := requestEvent{
		Subcomponent:    "codex",
		Transport:       "responses_http",
		RequestID:       "req-test",
		CursorRequestID: "cursor-req-test",
		Alias:           "gpt-5",
		Model:           "gpt-5",
		URL:             "https://example/codex/v1/responses",
		BodyBytes:       42,
		Body:            `{"hello":"world"}`,
		BodyB64:         "eyJoZWxsbyI6IndvcmxkIn0=",
	}
	logCodexEvent(context.Background(), slog.LevelDebug, "codex.responses.request", ev.toSlogAttrs())

	got, err := os.ReadFile(sinkPath)
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	if !strings.Contains(string(got), "codex.responses.request") {
		t.Errorf("sink missing event name: %s", string(got))
	}
	if !strings.Contains(string(got), `"request_id":"req-test"`) {
		t.Errorf("sink missing request_id: %s", string(got))
	}
	if !strings.Contains(string(got), `"cursor_request_id":"cursor-req-test"`) {
		t.Errorf("sink missing cursor_request_id: %s", string(got))
	}
	if !strings.Contains(string(got), `"body":"{\"hello\":\"world\"}"`) {
		t.Errorf("sink missing body bytes: %s", string(got))
	}
}

func TestSummarizeWsRequestPreservesPreviousResponseFlag(t *testing.T) {
	payload := ResponseCreateWsRequest{
		Model:              "gpt-5.4",
		Instructions:       "rules",
		Input:              []map[string]any{{"role": "user"}},
		PromptCacheKey:     "cursor:conv-x",
		PreviousResponseID: "resp-prev",
		Stream:             true,
		ServiceTier:        "priority",
	}
	s := summarizeWsRequest(payload)
	if !s.PreviousResponseID {
		t.Fatalf("PreviousResponseID flag should be true")
	}
	if s.PromptCacheKey != "cursor:conv-x" {
		t.Errorf("prompt_cache_key=%q want cursor:conv-x", s.PromptCacheKey)
	}
}

func TestSummarizeFinalResponseCreateFrameCapturesPromptMarkersAndShape(t *testing.T) {
	instructions := strings.Join([]string{
		"cursor system rules",
		"<cursor_mode>agent</cursor_mode>",
		"You are Codex, a coding agent based on GPT-5.",
		"vivid inner life as Codex",
	}, "\n\n")
	payload := ResponseCreateWsRequest{
		Type:         "response.create",
		Model:        "gpt-5.4",
		Instructions: instructions,
		Input: []map[string]any{
			{"type": "message", "role": "developer"},
			{"type": "message", "role": "user"},
			{"type": "function_call", "name": "shell_command"},
		},
		Tools: []any{
			map[string]any{"type": "function", "name": "shell_command"},
			map[string]any{"type": "local_shell"},
		},
		PromptCacheKey:     "cursor:conv-x",
		PreviousResponseID: "resp-prev",
		ServiceTier:        "priority",
	}
	frame, err := MarshalResponseCreateWsRequest(payload)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	summary := summarizeFinalResponseCreateFrame(WebsocketTransportConfig{
		RequestID:       "req-123",
		CursorRequestID: "cursor-req-123",
		Alias:           "clyde-gpt-5.4-1m-medium",
	}, payload, frame)

	if summary.RequestID != "req-123" || summary.CursorRequestID != "cursor-req-123" {
		t.Fatalf("request ids not preserved: %#v", summary)
	}
	if summary.Alias != "clyde-gpt-5.4-1m-medium" || summary.Model != "gpt-5.4" {
		t.Fatalf("model metadata not preserved: %#v", summary)
	}
	if summary.InstructionsLength != len(instructions) {
		t.Fatalf("instructions_length=%d want %d", summary.InstructionsLength, len(instructions))
	}
	if summary.InstructionsSHA256 != sha256StringHex(instructions) {
		t.Fatalf("instructions_sha256=%q want %q", summary.InstructionsSHA256, sha256StringHex(instructions))
	}
	if summary.FrameBytes != len(frame) || summary.FrameSHA256 != sha256Hex(frame) {
		t.Fatalf("frame fingerprint mismatch: %#v", summary)
	}
	if !summary.CursorSystemPromptPresent {
		t.Fatalf("cursor system prompt marker should be present")
	}
	if !summary.ClydeCursorModePresent {
		t.Fatalf("clyde cursor mode marker should be present")
	}
	if !summary.OldClydePersonalityPromptPresent {
		t.Fatalf("old Clyde personality marker should be present")
	}
	if !summary.CodexBasePromptPresent {
		t.Fatalf("Codex base prompt marker should be present")
	}
	if summary.InputCount != 3 || summary.ToolCount != 2 {
		t.Fatalf("counts = input:%d tools:%d", summary.InputCount, summary.ToolCount)
	}
	if summary.InputTypeCounts["message"] != 2 || summary.InputTypeCounts["function_call"] != 1 {
		t.Fatalf("input type counts: %#v", summary.InputTypeCounts)
	}
	if summary.InputRoleCounts["developer"] != 1 || summary.InputRoleCounts["user"] != 1 {
		t.Fatalf("input role counts: %#v", summary.InputRoleCounts)
	}
	if got := strings.Join(summary.ToolNames, ","); got != "local_shell,shell_command" {
		t.Fatalf("tool names=%q", got)
	}
	if !summary.PreviousResponseIDPresent {
		t.Fatalf("previous_response_id_present should be true")
	}
}

func TestLogWebsocketFrameSummaryDoesNotRequireBodyLogging(t *testing.T) {
	dir := t.TempDir()
	sinkPath := filepath.Join(dir, "codex.jsonl")
	t.Setenv("CLYDE_CODEX_LOG_PATH", sinkPath)
	codexFileLoggerOnce = sync.Once{}
	codexFileLogger = nil
	t.Cleanup(func() {
		codexFileLoggerOnce = sync.Once{}
		codexFileLogger = nil
	})

	payload := ResponseCreateWsRequest{
		Type:         "response.create",
		Model:        "gpt-5.4",
		Instructions: "cursor system rules",
		Input:        []map[string]any{{"type": "message", "role": "user"}},
		Tools:        []any{map[string]any{"type": "function", "name": "shell_command"}},
	}
	frame, err := MarshalResponseCreateWsRequest(payload)
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}

	logWebsocketFrame(context.Background(), WebsocketTransportConfig{
		RequestID:       "req-summary",
		CursorRequestID: "cursor-req-summary",
		Alias:           "clyde-gpt-5.4-1m-medium",
		BodyLog:         BodyLogConfig{Mode: BodyLogOff},
	}, payload, frame, false)

	got, err := os.ReadFile(sinkPath)
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	text := string(got)
	if !strings.Contains(text, "adapter.codex.response_create_frame.summary") {
		t.Fatalf("sink missing summary event: %s", text)
	}
	if !strings.Contains(text, `"concern":"adapter.providers.codex.websocket"`) {
		t.Fatalf("sink missing websocket concern: %s", text)
	}
	if !strings.Contains(text, `"instructions_sha256":"`+sha256StringHex(payload.Instructions)+`"`) {
		t.Fatalf("sink missing instructions fingerprint: %s", text)
	}
	if strings.Contains(text, `"body":`) || strings.Contains(text, "cursor system rules") {
		t.Fatalf("summary should not include raw body or instructions: %s", text)
	}
}
