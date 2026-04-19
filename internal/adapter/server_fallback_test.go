package adapter

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/config"
)

const fakeStream = `#!/usr/bin/env sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"pong"}]}}'
echo '{"type":"result","usage":{"input_tokens":4,"output_tokens":1}}'
`

const fakeFail = `#!/usr/bin/env sh
echo "boom" 1>&2
exit 9
`

func writeBin(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix shebang fake-binary tests")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// buildServer wires a Server with the fallback enabled and a
// user-supplied [adapter.models.fb-haiku] alias bound to
// backend="fallback". Tests can then POST a chat completion at
// /v1/chat/completions and assert end-to-end dispatch through the
// fallback path.
func buildServer(t *testing.T, fallbackBinary string, opts ...func(*config.AdapterConfig)) *Server {
	t.Helper()
	scratch := t.TempDir()
	cfg := config.AdapterConfig{
		Enabled:      true,
		DefaultModel: "fb-haiku",
		Impersonation: config.AdapterImpersonation{
			BetaHeader:         "x",
			UserAgent:          "y",
			SystemPromptPrefix: "z",
		},
		Families: map[string]config.AdapterFamily{
			"haiku-4-5": {
				Model:           "claude-haiku-4-5-20251001",
				ThinkingModes:   []string{"default"},
				MaxOutputTokens: 16000,
				SupportsTools:   boolPtr(true),
				SupportsVision:  boolPtr(true),
				Contexts: []config.AdapterModelContext{
					{Tokens: 200000},
				},
			},
		},
		Models: map[string]config.AdapterModel{
			"fb-haiku": {
				Backend: BackendFallback,
				Model:   "haiku",
				Context: 200000,
			},
		},
		Fallback: config.AdapterFallback{
			Enabled:           true,
			Trigger:           FallbackTriggerExplicit,
			Binary:            fallbackBinary,
			Timeout:           "10s",
			MaxConcurrent:     2,
			AllowedFamilies:   []string{"haiku-4-5"},
			ScratchSubdir:     "fallback",
			StreamPassthrough: true,
			DropUnsupported:   true,
			SuppressHookEnv:   true,
			FailureEscalation: FallbackEscalationFallbackError,
			CLIAliases: map[string]string{
				"haiku-4-5": "haiku",
			},
		},
	}
	for _, o := range opts {
		o(&cfg)
	}
	deps := Deps{
		ScratchDir: func() string { return scratch },
	}
	srv, err := New(cfg, deps, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func postChat(t *testing.T, srv *Server, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	return w
}

func TestServerFallbackExplicitDispatch(t *testing.T) {
	bin := writeBin(t, fakeStream)
	srv := buildServer(t, bin)
	w := postChat(t, srv, map[string]any{
		"model": "fb-haiku",
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp ChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("no choices: %s", w.Body.String())
	}
	var content string
	if err := json.Unmarshal(resp.Choices[0].Message.Content, &content); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if content != "pong" {
		t.Fatalf("content = %q want pong", content)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestServerFallbackExplicitDispatchSurfacesFailure(t *testing.T) {
	bin := writeBin(t, fakeFail)
	srv := buildServer(t, bin)
	w := postChat(t, srv, map[string]any{
		"model": "fb-haiku",
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
	})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "fallback_error") {
		t.Fatalf("body = %s want fallback_error", w.Body.String())
	}
}

func TestServerFallbackExplicitStreamRejectedWhenPassthroughDisabled(t *testing.T) {
	bin := writeBin(t, fakeStream)
	srv := buildServer(t, bin, func(c *config.AdapterConfig) {
		c.Fallback.StreamPassthrough = false
	})
	w := postChat(t, srv, map[string]any{
		"model":  "fb-haiku",
		"stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "stream_passthrough") {
		t.Fatalf("body missing stream_passthrough hint: %s", w.Body.String())
	}
}

func TestServerFallbackExplicitStreamSucceeds(t *testing.T) {
	bin := writeBin(t, fakeStream)
	srv := buildServer(t, bin)
	w := postChat(t, srv, map[string]any{
		"model":  "fb-haiku",
		"stream": true,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("no SSE frames in body: %s", body)
	}
	if !strings.Contains(body, "pong") {
		t.Fatalf("missing assistant text: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("missing [DONE]: %s", body)
	}
}

func TestSurfaceFallbackFailurePicksError(t *testing.T) {
	srv := &Server{
		cfg: config.AdapterConfig{
			Fallback: config.AdapterFallback{
				FailureEscalation: FallbackEscalationOAuthError,
			},
		},
	}
	w := httptest.NewRecorder()
	srv.surfaceFallbackFailure(w, errOAuth("oauth boom"), errOAuth("fb boom"))
	if !strings.Contains(w.Body.String(), "oauth boom") {
		t.Fatalf("expected oauth_error surfaced; body = %s", w.Body.String())
	}

	srv.cfg.Fallback.FailureEscalation = FallbackEscalationFallbackError
	w = httptest.NewRecorder()
	srv.surfaceFallbackFailure(w, errOAuth("oauth boom"), errOAuth("fb boom"))
	if !strings.Contains(w.Body.String(), "fb boom") {
		t.Fatalf("expected fallback_error surfaced; body = %s", w.Body.String())
	}
}

// errOAuth is a tiny helper to avoid importing errors / fmt in
// the surfacing test purely for one literal.
type errOAuth string

func (e errOAuth) Error() string { return string(e) }
