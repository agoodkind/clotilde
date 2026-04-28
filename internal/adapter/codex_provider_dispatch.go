package adapter

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

// dispatchCodexProvider routes a Codex-bound request through the new
// codex.Provider via the Server's provider.Registry. It is invoked
// by dispatchResolvedChat when the resolver successfully maps the
// request to ProviderCodex and a Codex provider is registered.
//
// Streaming and non-streaming share the same Provider.Execute call;
// the writer choice is what differs. The streaming writer forwards
// chunks over SSE in real time. The non-streaming writer buffers
// chunks and the merged ChatResponse is written once Execute
// returns.
func (s *Server) dispatchCodexProvider(
	w http.ResponseWriter,
	r *http.Request,
	req ChatRequest,
	model ResolvedModel,
	reqID string,
	cursorReq adaptercursor.Request,
	resolvedReq adapterresolver.ResolvedRequest,
) {
	started := time.Now()
	_ = cursorReq // resolvedReq.Cursor carries the same value; keep parameter for future hooks.

	s.emitRequestStarted(r.Context(), model, "direct", reqID, model.Alias, req.Stream)

	if req.Stream {
		s.dispatchCodexProviderStream(r.Context(), w, r, req, model, reqID, started, resolvedReq)
		return
	}
	s.dispatchCodexProviderCollect(r.Context(), w, req, model, reqID, started, resolvedReq)
}

func (s *Server) dispatchCodexProviderStream(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	req ChatRequest,
	model ResolvedModel,
	reqID string,
	started time.Time,
	resolvedReq adapterresolver.ResolvedRequest,
) {
	writer, err := newProviderStreamWriter(s, w)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	created := time.Now().Unix()

	s.emitRequestStreamOpened(r.Context(), model, "direct", reqID, model.Alias, true)

	result, runErr := s.codexProvider.Execute(ctx, resolvedReq, writer)
	if runErr != nil {
		adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
			Provider:   "codex_direct",
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.Alias,
			Stream:     true,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        runErr.Error(),
		})
		writeError(w, http.StatusBadGateway, "upstream_error", runErr.Error())
		return
	}

	usage := result.Usage
	if model.Context > 0 {
		usage.MaxTokens = model.Context
	}
	finishReason := result.FinishReason
	finishChunk := adapteropenai.StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model.Alias,
		Choices: []adapteropenai.StreamChoice{{Index: 0, Delta: adapteropenai.StreamDelta{}, FinishReason: stringPtrLocal(finishReason)}},
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		finishChunk.Usage = &usage
	}
	_ = writer.WriteStreamChunk(finishChunk)
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		_ = writer.WriteStreamChunk(adapteropenai.StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model.Alias,
			Choices: []adapteropenai.StreamChoice{},
			Usage:   &usage,
		})
	}
	_ = writer.finalizeStream()

	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.chat.completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int("cache_read_tokens", usage.CachedTokens()),
		slog.Int("cache_creation_tokens", 0),
		slog.Int("derived_cache_creation_tokens", result.DerivedCacheCreationTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", true),
		slog.String("backend", "codex"),
		slog.String("provider_path", "provider"),
		slog.Bool("reasoning_signaled", result.ReasoningSignaled),
		slog.Bool("reasoning_visible", result.ReasoningVisible),
	)
	adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
		Stage:                      adapterruntime.RequestStageCompleted,
		Provider:                   "codex_direct",
		Backend:                    model.Backend,
		RequestID:                  reqID,
		Alias:                      model.Alias,
		ModelID:                    model.Alias,
		Stream:                     true,
		FinishReason:               finishReason,
		TokensIn:                   usage.PromptTokens,
		TokensOut:                  usage.CompletionTokens,
		CacheReadTokens:            usage.CachedTokens(),
		CacheCreationTokens:        0,
		DerivedCacheCreationTokens: result.DerivedCacheCreationTokens,
		DurationMs:                 time.Since(started).Milliseconds(),
	})
}

func (s *Server) dispatchCodexProviderCollect(
	ctx context.Context,
	w http.ResponseWriter,
	req ChatRequest,
	model ResolvedModel,
	reqID string,
	started time.Time,
	resolvedReq adapterresolver.ResolvedRequest,
) {
	collector := newProviderCollectorWriter()
	result, runErr := s.codexProvider.Execute(ctx, resolvedReq, collector)
	if runErr != nil {
		adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
			Provider:   "codex_direct",
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.Alias,
			Stream:     false,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        runErr.Error(),
		})
		writeError(w, http.StatusBadGateway, "upstream_error", runErr.Error())
		return
	}
	runResult := adaptercodex.RunResult{
		Usage:                      result.Usage,
		FinishReason:               result.FinishReason,
		ReasoningSignaled:          result.ReasoningSignaled,
		ReasoningVisible:           result.ReasoningVisible,
		DerivedCacheCreationTokens: result.DerivedCacheCreationTokens,
	}
	merged := adaptercodex.MergeChunks(reqID, model.Alias, systemFingerprint, collector.chunks, runResult)
	usage := result.Usage
	if model.Context > 0 {
		usage.MaxTokens = model.Context
	}
	if merged.Usage != nil {
		merged.Usage.MaxTokens = usage.MaxTokens
	}
	writeJSON(w, http.StatusOK, merged)
	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.chat.completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int("cache_read_tokens", usage.CachedTokens()),
		slog.Int("cache_creation_tokens", 0),
		slog.Int("derived_cache_creation_tokens", result.DerivedCacheCreationTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", false),
		slog.String("backend", "codex"),
		slog.String("provider_path", "provider"),
		slog.Bool("reasoning_signaled", result.ReasoningSignaled),
		slog.Bool("reasoning_visible", result.ReasoningVisible),
	)
	adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
		Stage:                      adapterruntime.RequestStageCompleted,
		Provider:                   "codex_direct",
		Backend:                    model.Backend,
		RequestID:                  reqID,
		Alias:                      model.Alias,
		ModelID:                    model.Alias,
		Stream:                     false,
		FinishReason:               result.FinishReason,
		TokensIn:                   usage.PromptTokens,
		TokensOut:                  usage.CompletionTokens,
		CacheReadTokens:            usage.CachedTokens(),
		CacheCreationTokens:        0,
		DerivedCacheCreationTokens: result.DerivedCacheCreationTokens,
		DurationMs:                 time.Since(started).Milliseconds(),
	})
}

func stringPtrLocal(v string) *string { return &v }
