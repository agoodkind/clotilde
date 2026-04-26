package anthropicbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
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
	EmitStreamError(adapteropenai.ErrorBody) error
	WriteStreamDone() error
	HasCommittedHeaders() bool
}

type ResponseDispatcher interface {
	Log() *slog.Logger
	EmitRequestStarted(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	EmitRequestStreamOpened(context.Context, adaptermodel.ResolvedModel, string, string, string, bool)
	NewAnthropicSSEWriter(http.ResponseWriter) (ResponseSSEWriter, error)
	AnthropicStreamClient() StreamClient
	SystemFingerprint() string
	StreamChunkFromTooltrans(tooltrans.OpenAIStreamChunk) adapteropenai.StreamChunk
	StreamChunkHasVisibleContent(adapteropenai.StreamChunk) bool
	TrackAnthropicContextUsage(string, adapteropenai.Usage) TrackedUsage
	JSONCoercion(any) JSONCoercion
	WriteJSON(http.ResponseWriter, int, any)
	LogTerminal(context.Context, adapterruntime.RequestEvent)
	LogCacheUsageAnthropic(context.Context, string, string, string, anthropic.Usage)
	CacheTTL() string
	NoticesEnabled() bool
	ClaimNotice(string, time.Time) bool
	UnclaimNotice(string, time.Time)
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
) error {
	d.EmitRequestStarted(ctx, model, "oauth", reqID, req.Model, false)
	var notice *anthropic.Notice
	previousOnHeaders := req.OnHeaders
	req.OnHeaders = func(h http.Header) {
		if previousOnHeaders != nil {
			previousOnHeaders(h)
		}
		notice = adapterruntime.EvaluateNoticeFromHeaders(h, d.NoticesEnabled(), d.ClaimNotice)
	}
	var buf []tooltrans.OpenAIStreamChunk
	emit := func(ch tooltrans.OpenAIStreamChunk) error {
		buf = append(buf, ch)
		return nil
	}
	anthUsage, anthStopReason, finishReason, err := RunTranslatorStream(d.AnthropicStreamClient(), ctx, req, model, reqID, emit)
	if err != nil {
		adapterruntime.LogFailed(d.Log(), ctx, adapterruntime.FailedAttrs{
			Backend:    "anthropic",
			Provider:   "anthropic-oauth",
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Err:        err,
			DurationMs: time.Since(started).Milliseconds(),
		})
		d.LogTerminal(ctx, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
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
		if notice != nil {
			if escalate {
				d.UnclaimNotice(notice.Kind, notice.ResetsAt)
				d.Log().LogAttrs(ctx, slog.LevelDebug, "adapter.notice.unclaimed_on_escalate",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", notice.Kind),
				)
			} else {
				errMsg = errMsg + " · " + notice.Text
				d.Log().LogAttrs(ctx, slog.LevelInfo, "adapter.notice.injected_into_error",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", notice.Kind),
					slog.String("notice_text", notice.Text),
				)
			}
		}
		return adapterruntime.EscalateOrWrite(
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
	rawUsage := usageWithContextWindow(UsageFromAnthropic(anthUsage), model.Context)
	tracked := d.TrackAnthropicContextUsage(trackerKey, rawUsage)
	u := tracked.Usage
	resp := MergeStreamChunks(reqID, model.Alias, d.SystemFingerprint(), buf, u, finishReason, d.JSONCoercion(jsonSpec), anthStopReason)
	resp, _ = adapterruntime.NoticeForResponseHeaders(resp, notice, func(kind string, resetsAt time.Time) {
		d.UnclaimNotice(kind, resetsAt)
	}, json.Marshal)
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
	adapterruntime.LogCompleted(d.Log(), ctx, adapterruntime.CompletedAttrs{
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
	breakdown := adapterruntime.EstimateCost(adapterruntime.CostInputs{
		ModelID:             req.Model,
		TTL:                 d.CacheTTL(),
		InputTokens:         u.PromptTokens,
		OutputTokens:        u.CompletionTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
	})
	d.LogTerminal(ctx, adapterruntime.RequestEvent{
		Stage:               adapterruntime.RequestStageCompleted,
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
) error {
	d.EmitRequestStarted(r.Context(), model, "oauth", reqID, req.Model, true)
	sw, err := d.NewAnthropicSSEWriter(w)
	if err != nil {
		return adapterruntime.EscalateOrWrite(
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
	previousOnHeaders := req.OnHeaders
	req.OnHeaders = func(h http.Header) {
		if previousOnHeaders != nil {
			previousOnHeaders(h)
		}
		notice, err := adapterruntime.NoticeForStreamHeaders(
			reqID,
			model.Alias,
			h,
			d.NoticesEnabled(),
			func(chunk tooltrans.OpenAIStreamChunk) error {
				return emit(d.StreamChunkFromTooltrans(chunk))
			},
			d.ClaimNotice,
			d.UnclaimNotice,
		)
		if err != nil && notice != nil {
			d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.notice.stream_emit_failed",
				slog.String("request_id", reqID),
				slog.String("alias", model.Alias),
				slog.String("model", req.Model),
				slog.String("kind", notice.Kind),
			)
		}
	}
	anthUsage, _, finishReason, err := RunTranslatorStream(d.AnthropicStreamClient(), r.Context(), req, model, reqID, func(ch tooltrans.OpenAIStreamChunk) error {
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
			// When the upstream surfaces a typed
			// *anthropic.UpstreamError, emit a native OpenAI error
			// envelope (data: {"error":{...}}) so Cursor and other
			// OpenAI clients see a structured error rather than an
			// assistant-shaped chat message. Untyped errors (e.g.
			// claude-fallback subprocess failures) keep the previous
			// actionable assistant text path.
			if ue, ok := anthropic.AsUpstreamError(err); ok {
				_ = sw.EmitStreamError(buildErrorBodyForUpstream(ue))
			} else {
				_ = EmitActionableStreamError(emit, reqID, model.Alias, err)
			}
			finishReason = "stop"
		}
		d.LogTerminal(r.Context(), adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
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
	rawFinalUsage := usageWithContextWindow(UsageFromAnthropic(anthUsage), model.Context)
	tracked := d.TrackAnthropicContextUsage(trackerKey, rawFinalUsage)
	finalUsage := tracked.Usage
	finishChunk := adapteropenai.StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model.Alias,
		Choices: []adapteropenai.StreamChoice{{Index: 0, Delta: adapteropenai.StreamDelta{}, FinishReason: stringPtr(finishReason)}},
	}
	if includeUsage {
		finishChunk.Usage = &finalUsage
	}
	_ = sw.EmitStreamChunk(d.SystemFingerprint(), finishChunk)
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
	adapterruntime.LogCompleted(d.Log(), r.Context(), adapterruntime.CompletedAttrs{
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
	breakdown := adapterruntime.EstimateCost(adapterruntime.CostInputs{
		ModelID:             req.Model,
		TTL:                 d.CacheTTL(),
		InputTokens:         finalUsage.PromptTokens,
		OutputTokens:        finalUsage.CompletionTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
	})
	if err == nil {
		d.LogTerminal(r.Context(), adapterruntime.RequestEvent{
			Stage:               adapterruntime.RequestStageCompleted,
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

func usageWithContextWindow(u adapteropenai.Usage, contextWindow int) adapteropenai.Usage {
	if contextWindow > 0 {
		u.MaxTokens = contextWindow
	}
	return u
}

func stringPtr(v string) *string { return &v }

// buildErrorBodyForUpstream maps a typed Anthropic *UpstreamError into
// the OpenAI ErrorBody shape used by both the SSE native error frame
// and the JSON error envelope.
//
// Type and code are derived from the four-class classification:
//   - retryable + 429 -> rate_limit_error
//   - retryable + 5xx -> server_error
//   - retryable + transport -> server_error (no upstream status)
//   - fatal -> upstream_error
//
// Message preserves the human-readable upstream text so users still
// see the friendly rate-limit body when one was available.
func buildErrorBodyForUpstream(ue *anthropic.UpstreamError) adapteropenai.ErrorBody {
	if ue == nil {
		return adapteropenai.ErrorBody{Type: "upstream_error", Message: "anthropic upstream error"}
	}
	body := adapteropenai.ErrorBody{Message: ue.Error()}
	switch {
	case ue.Class() == anthropic.ResponseClassRetryableError && ue.Status == 429:
		body.Type = "rate_limit_error"
		body.Code = "rate_limit_exceeded"
	case ue.Class() == anthropic.ResponseClassRetryableError:
		body.Type = "server_error"
		body.Code = "upstream_unavailable"
	default:
		body.Type = "upstream_error"
		body.Code = "upstream_failed"
	}
	return body
}
