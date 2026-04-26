package adapter

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
