package anthropicbackend

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/chatemit"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type TrackedUsage struct {
	Usage      adapteropenai.Usage
	RawPrompt  int
	RawTotal   int
	RolledFrom int
}

type ResponseSSEWriter interface {
	WriteSSEHeaders()
	EmitStreamChunk(string, adapteropenai.StreamChunk) error
	WriteStreamDone() error
	HasCommittedHeaders() bool
}

type ResponseDispatcher interface {
	Log() *slog.Logger
	EmitRequestStarted(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	EmitRequestStreamOpened(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	NewAnthropicSSEWriter(http.ResponseWriter) (ResponseSSEWriter, error)
	SystemFingerprint() string
	StreamChunkFromTooltrans(tooltrans.OpenAIStreamChunk) adapteropenai.StreamChunk
	StreamChunkHasVisibleContent(adapteropenai.StreamChunk) bool
	EmitActionableStreamError(func(adapteropenai.StreamChunk) error, string, string, error) error
	RunOAuthTranslatorStream(context.Context, anthropic.Request, adaptermodel.ResolvedModel, string, func(tooltrans.OpenAIStreamChunk) error) (anthropic.Usage, string, string, error)
	TrackAnthropicContextUsage(string, adapteropenai.Usage) TrackedUsage
	MergeAnthropicStreamChunks(string, string, []tooltrans.OpenAIStreamChunk, adapteropenai.Usage, string, any, string) any
	NoticeForResponseHeaders(any, *anthropic.Notice) (any, error)
	WriteJSON(http.ResponseWriter, int, any)
	LogTerminal(context.Context, chatemit.RequestEvent)
	LogCacheUsageAnthropic(context.Context, string, string, string, anthropic.Usage)
	CacheTTL() string
	UnclaimNotice(*anthropic.Notice)
}

func CollectResponse(
	d ResponseDispatcher,
	w http.ResponseWriter,
	ctx context.Context,
	req anthropic.Request,
	model adaptermodel.ResolvedModel,
	reqID string,
	started time.Time,
	jsonSpec any,
	escalate bool,
	trackerKey string,
	notice **anthropic.Notice,
) error {
	d.EmitRequestStarted(ctx, model, "oauth", reqID, req.Model, false)
	var buf []tooltrans.OpenAIStreamChunk
	emit := func(ch tooltrans.OpenAIStreamChunk) error {
		buf = append(buf, ch)
		return nil
	}
	anthUsage, anthStopReason, finishReason, err := d.RunOAuthTranslatorStream(ctx, req, model, reqID, emit)
	if err != nil {
		chatemit.LogFailed(d.Log(), ctx, chatemit.FailedAttrs{
			Backend:    "anthropic",
			Provider:   "anthropic-oauth",
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Err:        err,
			DurationMs: time.Since(started).Milliseconds(),
		})
		d.LogTerminal(ctx, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   "anthropic-oauth",
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Stream:     false,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
		errMsg := err.Error()
		if notice != nil && *notice != nil {
			if escalate {
				d.UnclaimNotice(*notice)
				d.Log().LogAttrs(ctx, slog.LevelDebug, "adapter.notice.unclaimed_on_escalate",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", (*notice).Kind),
				)
			} else {
				errMsg = errMsg + " · " + (*notice).Text
				d.Log().LogAttrs(ctx, slog.LevelInfo, "adapter.notice.injected_into_error",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", (*notice).Kind),
					slog.String("notice_text", (*notice).Text),
				)
			}
		}
		return chatemit.EscalateOrWrite(
			err,
			escalate,
			func(status int, code, msg string) error {
				d.WriteJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": code}})
				return nil
			},
			http.StatusBadGateway,
			"upstream_error",
			errMsg,
		)
	}
	rawUsage := UsageFromAnthropic(anthUsage)
	tracked := d.TrackAnthropicContextUsage(trackerKey, rawUsage)
	u := tracked.Usage
	resp := d.MergeAnthropicStreamChunks(reqID, model.Alias, buf, u, finishReason, jsonSpec, anthStopReason)
	resp, _ = d.NoticeForResponseHeaders(resp, derefNotice(notice))
	d.WriteJSON(w, http.StatusOK, resp)
	if u.PromptTokens != rawUsage.PromptTokens || u.TotalTokens != rawUsage.TotalTokens {
		d.Log().LogAttrs(ctx, slog.LevelInfo, "adapter.context_usage.tracked",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.Int("raw_prompt_tokens", tracked.RawPrompt),
			slog.Int("raw_total_tokens", tracked.RawTotal),
			slog.Int("rolled_output_tokens", tracked.RolledFrom),
			slog.Int("surfaced_prompt_tokens", u.PromptTokens),
			slog.Int("surfaced_total_tokens", u.TotalTokens),
		)
	}
	d.LogCacheUsageAnthropic(ctx, "anthropic", reqID, model.Alias, anthUsage)
	chatemit.LogCompleted(d.Log(), ctx, chatemit.CompletedAttrs{
		Backend:             "anthropic",
		Provider:            "anthropic-oauth",
		Path:                "oauth",
		SessionID:           reqID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        finishReason,
		TokensIn:            u.PromptTokens,
		TokensOut:           u.CompletionTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheTTL:            d.CacheTTL(),
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              false,
	})
	breakdown := chatemit.EstimateCost(chatemit.CostInputs{
		ModelID:             req.Model,
		TTL:                 d.CacheTTL(),
		InputTokens:         u.PromptTokens,
		OutputTokens:        u.CompletionTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
	})
	d.LogTerminal(ctx, chatemit.RequestEvent{
		Stage:               chatemit.RequestStageCompleted,
		Provider:            "anthropic-oauth",
		Backend:             model.Backend,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		Stream:              false,
		FinishReason:        finishReason,
		TokensIn:            u.PromptTokens,
		TokensOut:           u.CompletionTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CostMicrocents:      breakdown.TotalMicrocents,
		DurationMs:          time.Since(started).Milliseconds(),
	})
	return nil
}

func StreamResponse(
	d ResponseDispatcher,
	w http.ResponseWriter,
	r *http.Request,
	req anthropic.Request,
	model adaptermodel.ResolvedModel,
	reqID string,
	started time.Time,
	escalate bool,
	includeUsage bool,
	trackerKey string,
	noticeEmitter *func(tooltrans.OpenAIStreamChunk) error,
) error {
	d.EmitRequestStarted(r.Context(), model, "oauth", reqID, req.Model, true)
	sw, err := d.NewAnthropicSSEWriter(w)
	if err != nil {
		return chatemit.EscalateOrWrite(
			fmt.Errorf("no_flusher: streaming not supported by transport"),
			escalate,
			func(status int, code, msg string) error {
				d.WriteJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": code}})
				return nil
			},
			http.StatusInternalServerError,
			"no_flusher",
			err.Error(),
		)
	}
	sw.WriteSSEHeaders()
	d.EmitRequestStreamOpened(r.Context(), model, "oauth", reqID, req.Model, true)

	emittedContent := false
	emit := func(chunk adapteropenai.StreamChunk) error {
		if d.StreamChunkHasVisibleContent(chunk) {
			emittedContent = true
		}
		return sw.EmitStreamChunk(d.SystemFingerprint(), chunk)
	}
	if noticeEmitter != nil {
		*noticeEmitter = func(ch tooltrans.OpenAIStreamChunk) error {
			return emit(d.StreamChunkFromTooltrans(ch))
		}
	}
	anthUsage, _, finishReason, err := d.RunOAuthTranslatorStream(r.Context(), req, model, reqID, func(ch tooltrans.OpenAIStreamChunk) error {
		return emit(d.StreamChunkFromTooltrans(ch))
	})
	if err != nil {
		d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.chat.stream_error",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.Any("err", err),
		)
		if escalate && !sw.HasCommittedHeaders() {
			return err
		}
		if !emittedContent {
			_ = d.EmitActionableStreamError(emit, reqID, model.Alias, err)
			finishReason = "stop"
		}
		d.LogTerminal(r.Context(), chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   "anthropic-oauth",
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Stream:     true,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
	}
	if err == nil && !emittedContent && finishReason == "" && anthUsage.InputTokens == 0 && anthUsage.OutputTokens == 0 && anthUsage.CacheReadInputTokens == 0 && anthUsage.CacheCreationInputTokens == 0 {
		streamErr := fmt.Errorf("anthropic stream ended without content; claude authentication may be invalid")
		d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.chat.stream_empty",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.Any("err", streamErr),
		)
		_ = emit(adapteropenai.StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model.Alias,
			Choices: []adapteropenai.StreamChoice{{
				Index: 0,
				Delta: adapteropenai.StreamDelta{
					Role:    "assistant",
					Content: "Clyde adapter upstream stream ended before producing content. Claude authentication may be invalid. Run `claude /login`, then retry.",
				},
			}},
		})
		finishReason = "stop"
	}
	_ = sw.EmitStreamChunk(d.SystemFingerprint(), adapteropenai.StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model.Alias,
		Choices: []adapteropenai.StreamChoice{{Index: 0, Delta: adapteropenai.StreamDelta{}, FinishReason: stringPtr(finishReason)}},
	})
	rawFinalUsage := UsageFromAnthropic(anthUsage)
	tracked := d.TrackAnthropicContextUsage(trackerKey, rawFinalUsage)
	finalUsage := tracked.Usage
	if err == nil && emittedContent && finalUsage.PromptTokens == 0 && finalUsage.CompletionTokens == 0 {
		d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.anthropic.usage_missing",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.String("stop_reason", finishReason),
			slog.Int("raw_input_tokens", anthUsage.InputTokens),
			slog.Int("raw_output_tokens", anthUsage.OutputTokens),
			slog.Int("raw_cache_read_tokens", anthUsage.CacheReadInputTokens),
			slog.Int("raw_cache_creation_tokens", anthUsage.CacheCreationInputTokens),
		)
	}
	if finalUsage.PromptTokens != rawFinalUsage.PromptTokens || finalUsage.TotalTokens != rawFinalUsage.TotalTokens {
		d.Log().LogAttrs(r.Context(), slog.LevelInfo, "adapter.context_usage.tracked",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.Int("raw_prompt_tokens", tracked.RawPrompt),
			slog.Int("raw_total_tokens", tracked.RawTotal),
			slog.Int("rolled_output_tokens", tracked.RolledFrom),
			slog.Int("surfaced_prompt_tokens", finalUsage.PromptTokens),
			slog.Int("surfaced_total_tokens", finalUsage.TotalTokens),
		)
	}
	if includeUsage {
		_ = sw.EmitStreamChunk(d.SystemFingerprint(), adapteropenai.StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model.Alias,
			Choices: []adapteropenai.StreamChoice{},
			Usage:   &finalUsage,
		})
	}
	_ = sw.WriteStreamDone()

	d.LogCacheUsageAnthropic(r.Context(), "anthropic", reqID, model.Alias, anthUsage)
	chatemit.LogCompleted(d.Log(), r.Context(), chatemit.CompletedAttrs{
		Backend:             "anthropic",
		Provider:            "anthropic-oauth",
		Path:                "oauth",
		SessionID:           reqID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        finishReason,
		TokensIn:            finalUsage.PromptTokens,
		TokensOut:           finalUsage.CompletionTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheTTL:            d.CacheTTL(),
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              true,
	})
	breakdown := chatemit.EstimateCost(chatemit.CostInputs{
		ModelID:             req.Model,
		TTL:                 d.CacheTTL(),
		InputTokens:         finalUsage.PromptTokens,
		OutputTokens:        finalUsage.CompletionTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
	})
	if err == nil {
		d.LogTerminal(r.Context(), chatemit.RequestEvent{
			Stage:               chatemit.RequestStageCompleted,
			Provider:            "anthropic-oauth",
			Backend:             model.Backend,
			RequestID:           reqID,
			Alias:               model.Alias,
			ModelID:             req.Model,
			Stream:              true,
			FinishReason:        finishReason,
			TokensIn:            finalUsage.PromptTokens,
			TokensOut:           finalUsage.CompletionTokens,
			CacheReadTokens:     anthUsage.CacheReadInputTokens,
			CacheCreationTokens: anthUsage.CacheCreationInputTokens,
			CostMicrocents:      breakdown.TotalMicrocents,
			DurationMs:          time.Since(started).Milliseconds(),
		})
	}
	return nil
}

func derefNotice(v **anthropic.Notice) *anthropic.Notice {
	if v == nil {
		return nil
	}
	return *v
}

func stringPtr(v string) *string { return &v }
