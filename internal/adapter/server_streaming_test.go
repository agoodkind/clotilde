package adapter

import (
	"io"
	"log/slog"
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
