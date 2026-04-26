package codex

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

// Dispatcher is the narrow Codex backend surface the root adapter facade
// depends on. The package owns the backend entrypoint; the concrete direct/app
// transport behavior remains behind the implementation callbacks.
type Dispatcher interface {
	AppFallbackEnabled() bool
	RunCodexDirect(context.Context, adapteropenai.ChatRequest, adaptermodel.ResolvedModel, string, string, func(adapteropenai.StreamChunk) error) (any, error)
	RunCodexManaged(context.Context, adapteropenai.ChatRequest, adaptermodel.ResolvedModel, string, string, func(adapteropenai.StreamChunk) error) (any, string, bool, error)
	RunCodexAppFallback(context.Context, adapteropenai.ChatRequest, string, func(adapteropenai.StreamChunk) error) (any, error)
	EmitRequestStarted(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	EmitRequestStreamOpened(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	NewSSEWriter(http.ResponseWriter) (SSEWriter, error)
	StreamChunkFromTooltrans(adapteropenai.StreamChunk) adapteropenai.StreamChunk
	MergeChunks(string, string, []adapteropenai.StreamChunk, any) any
	WriteJSON(http.ResponseWriter, int, any)
	LogTerminal(context.Context, adapterruntime.RequestEvent)
	Log() *slog.Logger
	SystemFingerprint() string
	ResultUsage(any) *adapteropenai.Usage
	ResultFinishReason(any) string
	ResultReasoning(any) (bool, bool)
	ResultDerivedCacheCreationTokens(any) int
}

type SSEWriter interface {
	WriteSSEHeaders()
	EmitStreamChunk(string, adapteropenai.StreamChunk) error
	WriteStreamDone() error
}

func Dispatch(d Dispatcher, w http.ResponseWriter, r *http.Request, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string) error {
	started := time.Now()
	if req.Stream {
		return Stream(d, w, r, req, model, effort, reqID, started)
	}
	return Collect(d, w, r, req, model, effort, reqID, started)
}

func Collect(d Dispatcher, w http.ResponseWriter, r *http.Request, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string, started time.Time) error {
	var chunks []adapteropenai.StreamChunk
	path := "direct"
	d.EmitRequestStarted(r.Context(), model, path, reqID, model.Alias, false)
	emit := func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	}
	directRes, directErr := d.RunCodexDirect(r.Context(), req, model, effort, reqID, emit)
	path, res, _, err := resolveTransportSelection(d, r.Context(), req, model, reqID, started, chunks, directRes, directErr, false, func() (any, bool, error) {
		chunks = nil
		d.EmitRequestStarted(r.Context(), model, "app", reqID, model.Alias, false)
		assistantRes, assistantText, managedRun, runErr := d.RunCodexManaged(r.Context(), req, model, effort, reqID, emit)
		_ = assistantText
		if !managedRun && runErr == nil {
			assistantRes, runErr = d.RunCodexAppFallback(r.Context(), req, reqID, emit)
		}
		return assistantRes, managedRun, runErr
	})
	if err != nil {
		d.LogTerminal(r.Context(), adapterruntime.RequestEvent{Stage: adapterruntime.RequestStageFailed, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: false, DurationMs: time.Since(started).Milliseconds(), Err: err.Error()})
		return err
	}
	d.WriteJSON(w, http.StatusOK, d.MergeChunks(reqID, model.Alias, chunks, res))
	usage := d.ResultUsage(res)
	reasoningSignaled, reasoningVisible := d.ResultReasoning(res)
	d.Log().LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed", slog.String("request_id", reqID), slog.String("model", model.Alias), slog.Int("prompt_tokens", usage.PromptTokens), slog.Int("completion_tokens", usage.CompletionTokens), slog.Int("cache_read_tokens", usage.CachedTokens()), slog.Int("cache_creation_tokens", 0), slog.Int("derived_cache_creation_tokens", d.ResultDerivedCacheCreationTokens(res)), slog.Int64("duration_ms", time.Since(started).Milliseconds()), slog.Bool("stream", false), slog.String("backend", "codex"), slog.Bool("reasoning_signaled", reasoningSignaled), slog.Bool("reasoning_visible", reasoningVisible))
	d.LogTerminal(r.Context(), adapterruntime.RequestEvent{Stage: adapterruntime.RequestStageCompleted, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: false, FinishReason: d.ResultFinishReason(res), TokensIn: usage.PromptTokens, TokensOut: usage.CompletionTokens, CacheReadTokens: usage.CachedTokens(), CacheCreationTokens: 0, DerivedCacheCreationTokens: d.ResultDerivedCacheCreationTokens(res), DurationMs: time.Since(started).Milliseconds()})
	return nil
}

func Stream(d Dispatcher, w http.ResponseWriter, r *http.Request, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string, started time.Time) error {
	path := "direct"
	d.EmitRequestStarted(r.Context(), model, path, reqID, model.Alias, true)
	sw, err := d.NewSSEWriter(w)
	if err != nil {
		return err
	}
	sw.WriteSSEHeaders()
	d.EmitRequestStreamOpened(r.Context(), model, path, reqID, model.Alias, true)
	created := time.Now().Unix()
	var directChunks []adapteropenai.StreamChunk
	d.Log().LogAttrs(r.Context(), slog.LevelInfo, "adapter.codex.stream.mode",
		slog.String("request_id", reqID),
		slog.String("backend", "codex"),
		slog.String("alias", model.Alias),
		slog.String("model", model.ClaudeModel),
		slog.Bool("app_fallback", d.AppFallbackEnabled()),
		slog.Bool("direct_emit_live", !d.AppFallbackEnabled()),
	)
	directEmit := func(ch adapteropenai.StreamChunk) error {
		directChunks = append(directChunks, ch)
		if !d.AppFallbackEnabled() {
			return sw.EmitStreamChunk(d.SystemFingerprint(), d.StreamChunkFromTooltrans(ch))
		}
		return nil
	}

	directRes, directErr := d.RunCodexDirect(r.Context(), req, model, effort, reqID, directEmit)
	path, res, _, runErr := resolveTransportSelection(d, r.Context(), req, model, reqID, started, directChunks, directRes, directErr, true, func() (any, bool, error) {
		d.EmitRequestStarted(r.Context(), model, "app", reqID, model.Alias, true)
		d.EmitRequestStreamOpened(r.Context(), model, "app", reqID, model.Alias, true)
		var assistantText string
		emit := func(ch adapteropenai.StreamChunk) error {
			return sw.EmitStreamChunk(d.SystemFingerprint(), d.StreamChunkFromTooltrans(ch))
		}
		assistantRes, assistantText, managedRun, runErr := d.RunCodexManaged(r.Context(), req, model, effort, reqID, emit)
		_ = assistantText
		if !managedRun && runErr == nil {
			assistantRes, runErr = d.RunCodexAppFallback(r.Context(), req, reqID, emit)
		}
		return assistantRes, managedRun, runErr
	})
	if runErr != nil {
		d.LogTerminal(r.Context(), adapterruntime.RequestEvent{Stage: adapterruntime.RequestStageFailed, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: true, DurationMs: time.Since(started).Milliseconds(), Err: runErr.Error()})
		return runErr
	}
	if path == "direct" && d.AppFallbackEnabled() {
		for _, ch := range directChunks {
			if err := sw.EmitStreamChunk(d.SystemFingerprint(), d.StreamChunkFromTooltrans(ch)); err != nil {
				return err
			}
		}
	}
	usage := d.ResultUsage(res)
	if usage != nil && model.Context > 0 {
		usage.MaxTokens = model.Context
	}
	finishChunk := adapteropenai.StreamChunk{ID: reqID, Object: "chat.completion.chunk", Created: created, Model: model.Alias, Choices: []adapteropenai.StreamChoice{{Index: 0, Delta: adapteropenai.StreamDelta{}, FinishReason: stringPtr(d.ResultFinishReason(res))}}}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		finishChunk.Usage = usage
	}
	_ = sw.EmitStreamChunk(d.SystemFingerprint(), finishChunk)
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		_ = sw.EmitStreamChunk(d.SystemFingerprint(), adapteropenai.StreamChunk{ID: reqID, Object: "chat.completion.chunk", Created: created, Model: model.Alias, Choices: []adapteropenai.StreamChoice{}, Usage: usage})
	}
	_ = sw.WriteStreamDone()
	reasoningSignaled, reasoningVisible := d.ResultReasoning(res)
	d.Log().LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed", slog.String("request_id", reqID), slog.String("model", model.Alias), slog.Int("prompt_tokens", usage.PromptTokens), slog.Int("completion_tokens", usage.CompletionTokens), slog.Int("cache_read_tokens", usage.CachedTokens()), slog.Int("cache_creation_tokens", 0), slog.Int("derived_cache_creation_tokens", d.ResultDerivedCacheCreationTokens(res)), slog.Int64("duration_ms", time.Since(started).Milliseconds()), slog.Bool("stream", true), slog.String("backend", "codex"), slog.Bool("reasoning_signaled", reasoningSignaled), slog.Bool("reasoning_visible", reasoningVisible))
	d.LogTerminal(r.Context(), adapterruntime.RequestEvent{Stage: adapterruntime.RequestStageCompleted, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: true, FinishReason: d.ResultFinishReason(res), TokensIn: usage.PromptTokens, TokensOut: usage.CompletionTokens, CacheReadTokens: usage.CachedTokens(), CacheCreationTokens: 0, DerivedCacheCreationTokens: d.ResultDerivedCacheCreationTokens(res), DurationMs: time.Since(started).Milliseconds()})
	return nil
}

func stringPtr(v string) *string { return &v }

func providerName(model adaptermodel.ResolvedModel, route string) string {
	switch route {
	case "direct":
		return "codex_direct"
	case "app":
		return "codex_app"
	default:
		return "codex"
	}
}
