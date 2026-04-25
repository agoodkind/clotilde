package codex

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/chatemit"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

// Dispatcher is the narrow Codex backend surface the root adapter facade
// depends on. The package owns the backend entrypoint; the concrete direct/app
// transport behavior remains behind the implementation callbacks.
type Dispatcher interface {
	AppFallbackEnabled() bool
	RunCodexDirect(context.Context, adapteropenai.ChatRequest, adaptermodel.ResolvedModel, string, string, func(tooltrans.OpenAIStreamChunk) error) (any, error)
	RunCodexManaged(context.Context, adapteropenai.ChatRequest, adaptermodel.ResolvedModel, string, string, func(tooltrans.OpenAIStreamChunk) error) (any, string, bool, error)
	RunCodexAppFallback(context.Context, adapteropenai.ChatRequest, string, func(tooltrans.OpenAIStreamChunk) error) (any, error)
	ShouldEscalateDirect(adapteropenai.ChatRequest, []tooltrans.OpenAIStreamChunk, any) (bool, string)
	EmitRequestStarted(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	EmitRequestStreamOpened(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	NewSSEWriter(http.ResponseWriter) (SSEWriter, error)
	StreamChunkFromTooltrans(tooltrans.OpenAIStreamChunk) adapteropenai.StreamChunk
	MergeChunks(string, string, []tooltrans.OpenAIStreamChunk, any) any
	WriteJSON(http.ResponseWriter, int, any)
	LogTerminal(context.Context, chatemit.RequestEvent)
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
	var chunks []tooltrans.OpenAIStreamChunk
	path := "direct"
	d.EmitRequestStarted(r.Context(), model, path, reqID, model.Alias, false)
	emit := func(ch tooltrans.OpenAIStreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	}
	res, err := d.RunCodexDirect(r.Context(), req, model, effort, reqID, emit)
	managed := false
	if err == nil && d.AppFallbackEnabled() {
		if escalate, reason := d.ShouldEscalateDirect(req, chunks, res); escalate {
			d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.codex.direct.degraded",
				slog.String("request_id", reqID),
				slog.String("reason", reason),
				slog.String("alias", model.Alias),
				slog.String("model", model.ClaudeModel),
			)
			err = fmt.Errorf("codex direct degraded: %s", reason)
		}
	}
	if err != nil && d.AppFallbackEnabled() {
		d.LogTerminal(r.Context(), chatemit.RequestEvent{Stage: chatemit.RequestStageFailed, Provider: providerName(model, "direct"), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: false, DurationMs: time.Since(started).Milliseconds(), Err: err.Error()})
		d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.codex.fallback.escalating", slog.String("request_id", reqID), slog.Any("err", err))
		chunks = nil
		path = "app"
		d.EmitRequestStarted(r.Context(), model, path, reqID, model.Alias, false)
		var assistantText string
		res, assistantText, managed, err = d.RunCodexManaged(r.Context(), req, model, effort, reqID, emit)
		_ = assistantText
		if !managed && err == nil {
			res, err = d.RunCodexAppFallback(r.Context(), req, reqID, emit)
		}
	}
	if err != nil {
		d.LogTerminal(r.Context(), chatemit.RequestEvent{Stage: chatemit.RequestStageFailed, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: false, DurationMs: time.Since(started).Milliseconds(), Err: err.Error()})
		return err
	}
	d.WriteJSON(w, http.StatusOK, d.MergeChunks(reqID, model.Alias, chunks, res))
	usage := d.ResultUsage(res)
	reasoningSignaled, reasoningVisible := d.ResultReasoning(res)
	d.Log().LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed", slog.String("request_id", reqID), slog.String("model", model.Alias), slog.Int("prompt_tokens", usage.PromptTokens), slog.Int("completion_tokens", usage.CompletionTokens), slog.Int("cache_read_tokens", usage.CachedTokens()), slog.Int("cache_creation_tokens", 0), slog.Int("derived_cache_creation_tokens", d.ResultDerivedCacheCreationTokens(res)), slog.Int64("duration_ms", time.Since(started).Milliseconds()), slog.Bool("stream", false), slog.String("backend", "codex"), slog.Bool("reasoning_signaled", reasoningSignaled), slog.Bool("reasoning_visible", reasoningVisible))
	d.LogTerminal(r.Context(), chatemit.RequestEvent{Stage: chatemit.RequestStageCompleted, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: false, FinishReason: d.ResultFinishReason(res), TokensIn: usage.PromptTokens, TokensOut: usage.CompletionTokens, CacheReadTokens: usage.CachedTokens(), CacheCreationTokens: 0, DerivedCacheCreationTokens: d.ResultDerivedCacheCreationTokens(res), DurationMs: time.Since(started).Milliseconds()})
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
	var directChunks []tooltrans.OpenAIStreamChunk
	d.Log().LogAttrs(r.Context(), slog.LevelInfo, "adapter.codex.stream.mode",
		slog.String("request_id", reqID),
		slog.String("backend", "codex"),
		slog.String("alias", model.Alias),
		slog.String("model", model.ClaudeModel),
		slog.Bool("app_fallback", d.AppFallbackEnabled()),
		slog.Bool("direct_emit_live", !d.AppFallbackEnabled()),
	)
	directEmit := func(ch tooltrans.OpenAIStreamChunk) error {
		directChunks = append(directChunks, ch)
		if !d.AppFallbackEnabled() {
			return sw.EmitStreamChunk(d.SystemFingerprint(), d.StreamChunkFromTooltrans(ch))
		}
		return nil
	}

	res, runErr := d.RunCodexDirect(r.Context(), req, model, effort, reqID, directEmit)
	managed := false
	if runErr == nil && d.AppFallbackEnabled() {
		if escalate, reason := d.ShouldEscalateDirect(req, directChunks, res); escalate {
			d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.codex.direct.degraded",
				slog.String("request_id", reqID),
				slog.String("reason", reason),
				slog.String("alias", model.Alias),
				slog.String("model", model.ClaudeModel),
			)
			runErr = fmt.Errorf("codex direct degraded: %s", reason)
		}
	}
	if runErr != nil && d.AppFallbackEnabled() {
		d.LogTerminal(r.Context(), chatemit.RequestEvent{Stage: chatemit.RequestStageFailed, Provider: providerName(model, "direct"), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: true, DurationMs: time.Since(started).Milliseconds(), Err: runErr.Error()})
		d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.codex.fallback.escalating", slog.String("request_id", reqID), slog.Any("err", runErr))
		path = "app"
		d.EmitRequestStarted(r.Context(), model, path, reqID, model.Alias, true)
		d.EmitRequestStreamOpened(r.Context(), model, path, reqID, model.Alias, true)
		var assistantText string
		emit := func(ch tooltrans.OpenAIStreamChunk) error {
			return sw.EmitStreamChunk(d.SystemFingerprint(), d.StreamChunkFromTooltrans(ch))
		}
		res, assistantText, managed, runErr = d.RunCodexManaged(r.Context(), req, model, effort, reqID, emit)
		_ = assistantText
		if !managed && runErr == nil {
			res, runErr = d.RunCodexAppFallback(r.Context(), req, reqID, emit)
		}
	}
	if runErr != nil {
		d.LogTerminal(r.Context(), chatemit.RequestEvent{Stage: chatemit.RequestStageFailed, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: true, DurationMs: time.Since(started).Milliseconds(), Err: runErr.Error()})
		return runErr
	}
	if path == "direct" && d.AppFallbackEnabled() {
		for _, ch := range directChunks {
			if err := sw.EmitStreamChunk(d.SystemFingerprint(), d.StreamChunkFromTooltrans(ch)); err != nil {
				return err
			}
		}
	}
	_ = sw.EmitStreamChunk(d.SystemFingerprint(), adapteropenai.StreamChunk{ID: reqID, Object: "chat.completion.chunk", Created: created, Model: model.Alias, Choices: []adapteropenai.StreamChoice{{Index: 0, Delta: adapteropenai.StreamDelta{}, FinishReason: stringPtr(d.ResultFinishReason(res))}}})
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		_ = sw.EmitStreamChunk(d.SystemFingerprint(), adapteropenai.StreamChunk{ID: reqID, Object: "chat.completion.chunk", Created: created, Model: model.Alias, Choices: []adapteropenai.StreamChoice{}, Usage: d.ResultUsage(res)})
	}
	_ = sw.WriteStreamDone()
	usage := d.ResultUsage(res)
	reasoningSignaled, reasoningVisible := d.ResultReasoning(res)
	d.Log().LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed", slog.String("request_id", reqID), slog.String("model", model.Alias), slog.Int("prompt_tokens", usage.PromptTokens), slog.Int("completion_tokens", usage.CompletionTokens), slog.Int("cache_read_tokens", usage.CachedTokens()), slog.Int("cache_creation_tokens", 0), slog.Int("derived_cache_creation_tokens", d.ResultDerivedCacheCreationTokens(res)), slog.Int64("duration_ms", time.Since(started).Milliseconds()), slog.Bool("stream", true), slog.String("backend", "codex"), slog.Bool("reasoning_signaled", reasoningSignaled), slog.Bool("reasoning_visible", reasoningVisible))
	d.LogTerminal(r.Context(), chatemit.RequestEvent{Stage: chatemit.RequestStageCompleted, Provider: providerName(model, path), Backend: model.Backend, RequestID: reqID, Alias: model.Alias, ModelID: model.Alias, Stream: true, FinishReason: d.ResultFinishReason(res), TokensIn: usage.PromptTokens, TokensOut: usage.CompletionTokens, CacheReadTokens: usage.CachedTokens(), CacheCreationTokens: 0, DerivedCacheCreationTokens: d.ResultDerivedCacheCreationTokens(res), DurationMs: time.Since(started).Milliseconds()})
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
