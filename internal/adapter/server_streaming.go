package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// errSSENoFlusher indicates the ResponseWriter does not support flushing.
var errSSENoFlusher = errors.New("streaming not supported by this transport")

// sseWriter wraps an HTTP response for text/event-stream with idempotent headers.
type sseWriter struct {
	w                http.ResponseWriter
	f                http.Flusher
	headersCommitted bool
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, errSSENoFlusher
	}
	return &sseWriter{w: w, f: f}, nil
}

func (sw *sseWriter) writeSSEHeaders() {
	if sw.headersCommitted {
		return
	}
	sw.w.Header().Set("Content-Type", "text/event-stream")
	sw.w.Header().Set("Cache-Control", "no-cache")
	sw.w.Header().Set("Connection", "keep-alive")
	sw.w.WriteHeader(http.StatusOK)
	sw.headersCommitted = true
}

func (sw *sseWriter) emitStreamChunk(systemFingerprint string, chunk StreamChunk) error {
	sw.writeSSEHeaders()
	chunk.SystemFingerprint = systemFingerprint
	b, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(sw.w, "data: %s\n\n", b); err != nil {
		return err
	}
	sw.f.Flush()
	return nil
}

func (sw *sseWriter) writeStreamDone() error {
	if _, err := io.WriteString(sw.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	sw.f.Flush()
	return nil
}

func (sw *sseWriter) hasCommittedHeaders() bool {
	return sw.headersCommitted
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, stdout io.ReadCloser, reqID string, started time.Time) {
	sw, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no_flusher", "streaming not supported by this transport")
		return
	}
	sw.writeSSEHeaders()

	sink := func(chunk StreamChunk) error {
		return sw.emitStreamChunk(systemFingerprint, chunk)
	}

	usage, err := TranslateStream(stdout, model.Alias, reqID, sink)
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "stream translate error",
			slog.String("request_id", reqID),
			slog.Any("err", err),
		)
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		_ = sink(StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model.Alias,
			Choices: []StreamChoice{},
			Usage:   &usage,
		})
	}
	_ = sw.writeStreamDone()

	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", true),
	)
}
