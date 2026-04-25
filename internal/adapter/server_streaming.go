package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/chatemit"
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
	s.emitRequestStarted(r.Context(), model, "", reqID, model.ClaudeModel, true)
	sw, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no_flusher", "streaming not supported by this transport")
		return
	}
	sw.writeSSEHeaders()
	s.emitRequestStreamOpened(r.Context(), model, "", reqID, model.ClaudeModel, true)

	emittedContent := false
	sink := func(chunk StreamChunk) error {
		if streamChunkHasVisibleContent(chunk) {
			emittedContent = true
		}
		return sw.emitStreamChunk(systemFingerprint, chunk)
	}

	usage, finishReason, err := TranslateStream(stdout, model.Alias, reqID, sink)
	terminalStage := chatemit.RequestStageCompleted
	terminalErr := ""
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "stream translate error",
			slog.String("request_id", reqID),
			slog.Any("err", err),
		)
		if !emittedContent {
			_ = emitActionableStreamError(sink, reqID, model.Alias, err)
			_ = chatemit.EmitFinishChunk(sink, reqID, model.Alias, time.Now().Unix(), "stop")
			finishReason = "stop"
		}
		terminalStage = chatemit.RequestStageFailed
		terminalErr = err.Error()
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
		slog.String("finish_reason", finishReason),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int("cache_read_tokens", usage.CachedTokens()),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", true),
	)
	chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
		Stage:           terminalStage,
		Provider:        providerName(model, ""),
		Backend:         model.Backend,
		RequestID:       reqID,
		Alias:           model.Alias,
		ModelID:         model.ClaudeModel,
		Stream:          true,
		FinishReason:    finishReason,
		TokensIn:        usage.PromptTokens,
		TokensOut:       usage.CompletionTokens,
		CacheReadTokens: usage.CachedTokens(),
		DurationMs:      time.Since(started).Milliseconds(),
		Err:             terminalErr,
	})
}

func streamChunkHasVisibleContent(chunk StreamChunk) bool {
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" || choice.Delta.Refusal != "" || choice.Delta.Reasoning != "" || choice.Delta.ReasoningContent != "" {
			return true
		}
	}
	return false
}

func emitActionableStreamError(emit func(StreamChunk) error, reqID, modelAlias string, err error) error {
	return emit(StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelAlias,
		Choices: []StreamChoice{{
			Index: 0,
			Delta: StreamDelta{
				Role:    "assistant",
				Content: actionableStreamErrorMessage(err),
			},
		}},
	})
}

func actionableStreamErrorMessage(err error) string {
	msg := strings.ToLower(strings.TrimSpace(fmt.Sprint(err)))
	switch {
	case strings.Contains(msg, "oauth"), strings.Contains(msg, "login"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "401"):
		return "Clyde adapter upstream auth failed. Re-authenticate Claude with `claude /login`, then retry."
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "429"):
		return "Clyde adapter hit an upstream rate limit. Wait a moment and retry."
	default:
		return "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry."
	}
}
