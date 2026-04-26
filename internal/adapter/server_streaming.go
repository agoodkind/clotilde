package adapter

import (
	"io"
	"log/slog"
	"net/http"
	"time"

	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

var errSSENoFlusher = adapteropenai.ErrSSENoFlusher

type sseWriter = adapteropenai.SSEWriter

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) { return adapteropenai.NewSSEWriter(w) }

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, stdout io.ReadCloser, reqID string, started time.Time) {
	s.emitRequestStarted(r.Context(), model, "", reqID, model.ClaudeModel, true)
	sw, err := newSSEWriter(w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no_flusher", "streaming not supported by this transport")
		return
	}
	sw.WriteSSEHeaders()
	s.emitRequestStreamOpened(r.Context(), model, "", reqID, model.ClaudeModel, true)

	emittedContent := false
	sink := func(chunk StreamChunk) error {
		if streamChunkHasVisibleContent(chunk) {
			emittedContent = true
		}
		return sw.EmitStreamChunk(systemFingerprint, chunk)
	}

	usage, finishReason, err := TranslateStream(stdout, model.Alias, reqID, sink)
	terminalStage := adapterruntime.RequestStageCompleted
	terminalErr := ""
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "stream translate error",
			slog.String("request_id", reqID),
			slog.Any("err", err),
		)
		if !emittedContent {
			_ = emitActionableStreamError(sink, reqID, model.Alias, err)
			_ = adapterruntime.EmitFinishChunk(sink, reqID, model.Alias, time.Now().Unix(), "stop")
			finishReason = "stop"
		}
		terminalStage = adapterruntime.RequestStageFailed
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
	_ = sw.WriteStreamDone()

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
	adapterruntime.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, adapterruntime.RequestEvent{
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

// emitActionableStreamError forwards to the Anthropic-backend owned
// helper. The legacy local function existed because the actionable
// message branches on `*anthropic.UpstreamError`, which is Anthropic
// vocabulary; Phase 3 moved that ownership into the backend package.
func emitActionableStreamError(emit func(StreamChunk) error, reqID, modelAlias string, err error) error {
	return anthropicbackend.EmitActionableStreamError(emit, reqID, modelAlias, err)
}

func actionableStreamErrorMessage(err error) string {
	return anthropicbackend.ActionableStreamErrorMessage(err)
}
