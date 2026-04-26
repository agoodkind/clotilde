package adapter

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
)

// TestActionableStreamErrorMessageRoutesByClass locks in the
// regression that motivated the four-class classifier: errors whose
// string body happens to contain "rate limit" must not be rewritten
// as a misleading "upstream rate limit" assistant message unless the
// underlying error is a real *anthropic.UpstreamError with status
// 429. Plain errors fall back to the generic message.
func TestActionableStreamErrorMessageRoutesByClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "real_429_upstream_error_keeps_rate_limit_text",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}, nil),
				Status:         http.StatusTooManyRequests,
				Message:        "rate limited",
			},
			want: "Clyde adapter hit an upstream rate limit. Wait a moment and retry.",
		},
		{
			name: "real_401_upstream_error_keeps_auth_text",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}}, nil),
				Status:         http.StatusUnauthorized,
				Message:        "token expired",
			},
			want: "Clyde adapter upstream auth failed. Re-authenticate Claude with `claude /login`, then retry.",
		},
		{
			name: "fatal_4xx_upstream_error_uses_generic_text",
			err: &anthropic.UpstreamError{
				Classification: anthropic.Classify(&http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}}, nil),
				Status:         http.StatusBadRequest,
				Message:        "bad request",
			},
			want: "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry.",
		},
		{
			// Regression guard: a bare error containing "rate limit"
			// must NOT be rewritten as the rate-limit message. The
			// classifier owns that decision; the string contents do
			// not.
			name: "bare_error_with_rate_limit_word_uses_generic_text",
			err:  errors.New("downstream subprocess complained about rate limit fairness"),
			want: "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry.",
		},
		{
			// Regression guard: a bare error containing "429" must NOT
			// be rewritten as the rate-limit message.
			name: "bare_error_with_429_token_uses_generic_text",
			err:  errors.New("agent reported 429 children in payload"),
			want: "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry.",
		},
		{
			name: "bare_oauth_error_keeps_auth_text",
			err:  errors.New("oauth: refresh failed"),
			want: "Clyde adapter upstream auth failed. Re-authenticate Claude with `claude /login`, then retry.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionableStreamErrorMessage(tc.err); got != tc.want {
				t.Fatalf("actionableStreamErrorMessage(%v) = %q\nwant: %q", tc.err, got, tc.want)
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read(_ []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestStreamChatSurfacesActionableErrorChunk(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()

	srv.streamChat(
		w,
		req,
		ChatRequest{Stream: true},
		ResolvedModel{Alias: "clyde-haiku"},
		io.NopCloser(errorReader{}),
		"req-123",
		time.Now(),
	)

	body := w.Body.String()
	if !strings.Contains(body, "Clyde adapter request failed upstream") {
		t.Fatalf("missing actionable error chunk: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("missing stream terminator: %s", body)
	}
}

type flushRecorder struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
	flushes    int
}

func (r *flushRecorder) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *flushRecorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}

func (r *flushRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
}

func (r *flushRecorder) Flush() {
	r.flushes++
}

func TestStreamChatFlushesHeadersEveryChunkAndDone(t *testing.T) {
	srv := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := &flushRecorder{}

	srv.streamChat(
		w,
		req,
		ChatRequest{Stream: true},
		ResolvedModel{Alias: "clyde-opus"},
		io.NopCloser(strings.NewReader(fixtureStream)),
		"req-stream",
		time.Now(),
	)

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q want text/event-stream", got)
	}
	if w.statusCode != http.StatusOK {
		t.Fatalf("status = %d want 200", w.statusCode)
	}
	if w.flushes != 5 {
		t.Fatalf("flushes = %d want 5", w.flushes)
	}
	body := w.body.String()
	if strings.Count(body, "data: ") != 4 {
		t.Fatalf("stream body frame count = %d want 4 body=%q", strings.Count(body, "data: "), body)
	}
	if !strings.Contains(body, `"content":"hello "`) || !strings.Contains(body, `"content":"world"`) {
		t.Fatalf("body missing visible chunks: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("body missing done frame: %s", body)
	}
}
