package anthropicbackend

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic/fallback"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

type FallbackClient interface {
	CollectOpenAI(context.Context, fallback.Request, fallback.CollectOpenAIInput) (fallback.CollectOpenAIResult, error)
	StreamOpenAI(context.Context, fallback.Request, fallback.StreamOpenAIInput, func(adapteropenai.StreamChunk) error) (fallback.StreamOpenAIResult, error)
}

type FallbackResponseDispatcher interface {
	ResponseDispatcher
	WriteError(http.ResponseWriter, int, string, string)
	FallbackClient() FallbackClient
	LogCacheUsageFallback(context.Context, string, string, string, int, int, int)
}

func CollectFallbackResponse(
	d FallbackResponseDispatcher,
	w http.ResponseWriter,
	ctx context.Context,
	req fallback.Request,
	model adaptermodel.ResolvedModel,
	reqID string,
	started time.Time,
	jsonSpec any,
	escalate bool,
) error {
	d.EmitRequestStarted(ctx, model, "fallback", reqID, req.Model, false)
	run, err := d.FallbackClient().CollectOpenAI(ctx, req, fallback.CollectOpenAIInput{
		RequestID:         reqID,
		ModelAlias:        model.Alias,
		SystemFingerprint: d.SystemFingerprint(),
		CoerceText:        fallbackTextCoercer(d.JSONCoercion(jsonSpec)),
	})
	if err != nil {
		logFallbackFailure(d, ctx, model, req, reqID, started, false, err)
		return adapterruntime.EscalateOrWrite(
			err,
			escalate,
			func(status int, code, msg string) error {
				d.WriteError(w, status, code, msg)
				return nil
			},
			http.StatusBadGateway,
			"fallback_error",
			err.Error(),
		)
	}
	raw := run.Raw
	final := run.Final
	d.WriteJSON(w, http.StatusOK, final.Response)
	logFallbackCompletion(d, ctx, req, model, reqID, started, final.FinishReason, final.Usage, raw.Usage, false)
	logFallbackTerminalCompletion(d, ctx, req, model, reqID, started, final.FinishReason, final.Usage, raw.Usage, false)
	return nil
}

func StreamFallbackResponse(
	d FallbackResponseDispatcher,
	w http.ResponseWriter,
	r *http.Request,
	req fallback.Request,
	model adaptermodel.ResolvedModel,
	reqID string,
	started time.Time,
	escalate bool,
	includeUsage bool,
) error {
	ctx := r.Context()
	d.EmitRequestStarted(ctx, model, "fallback", reqID, req.Model, true)
	sw, err := d.NewAnthropicSSEWriter(w)
	if err != nil {
		return adapterruntime.EscalateOrWrite(
			fmt.Errorf("no_flusher: streaming not supported by transport"),
			escalate,
			func(status int, code, msg string) error {
				d.WriteError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"no_flusher",
			"streaming not supported by this transport",
		)
	}

	sw.WriteSSEHeaders()
	d.EmitRequestStreamOpened(ctx, model, "fallback", reqID, req.Model, true)

	created := time.Now().Unix()
	emit := func(chunk adapteropenai.StreamChunk) error {
		return sw.EmitStreamChunk(d.SystemFingerprint(), chunk)
	}

	run, streamErr := d.FallbackClient().StreamOpenAI(ctx, req, fallback.StreamOpenAIInput{
		RequestID:  reqID,
		ModelAlias: model.Alias,
		Created:    created,
	}, emit)
	if streamErr != nil {
		d.Log().LogAttrs(ctx, slog.LevelWarn, "adapter.chat.stream_error",
			slog.String("backend", "fallback"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("cli_model", req.Model),
			slog.Any("err", streamErr),
		)
		if escalate && !sw.HasCommittedHeaders() {
			return streamErr
		}
		d.LogTerminal(ctx, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
			Provider:   "anthropic-fallback",
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Stream:     true,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        streamErr.Error(),
		})
	}

	raw := run.Raw
	plan := run.Plan
	_ = adapterruntime.EmitFinishChunk(emit, reqID, model.Alias, created, plan.FinishReason)
	if includeUsage {
		_ = adapterruntime.EmitUsageChunk(emit, reqID, model.Alias, created, plan.Usage)
	}
	_ = sw.WriteStreamDone()
	logFallbackCompletion(d, ctx, req, model, reqID, started, plan.FinishReason, plan.Usage, raw.Usage, true)
	if streamErr == nil {
		logFallbackTerminalCompletion(d, ctx, req, model, reqID, started, plan.FinishReason, plan.Usage, raw.Usage, true)
	}
	return nil
}

func fallbackTextCoercer(coercion JSONCoercion) fallback.TextCoercer {
	if coercion.Coerce == nil {
		return nil
	}
	return func(text string) (string, bool) {
		coerced := coercion.Coerce(text)
		if coercion.Validate == nil {
			return coerced, true
		}
		return coerced, coercion.Validate(coerced)
	}
}

func logFallbackFailure(
	d FallbackResponseDispatcher,
	ctx context.Context,
	model adaptermodel.ResolvedModel,
	req fallback.Request,
	reqID string,
	started time.Time,
	stream bool,
	err error,
) {
	adapterruntime.LogFailed(d.Log(), ctx, adapterruntime.FailedAttrs{
		Backend:    "fallback",
		Provider:   "anthropic-fallback",
		RequestID:  reqID,
		Alias:      model.Alias,
		ModelID:    req.Model,
		Err:        err,
		DurationMs: time.Since(started).Milliseconds(),
	})
	d.LogTerminal(ctx, adapterruntime.RequestEvent{
		Stage:      adapterruntime.RequestStageFailed,
		Provider:   "anthropic-fallback",
		Backend:    model.Backend,
		RequestID:  reqID,
		Alias:      model.Alias,
		ModelID:    req.Model,
		Stream:     stream,
		DurationMs: time.Since(started).Milliseconds(),
		Err:        err.Error(),
	})
}

func logFallbackCompletion(
	d FallbackResponseDispatcher,
	ctx context.Context,
	req fallback.Request,
	model adaptermodel.ResolvedModel,
	reqID string,
	started time.Time,
	finishReason string,
	usage adapteropenai.Usage,
	rawUsage fallback.Usage,
	stream bool,
) {
	d.LogCacheUsageFallback(ctx, "fallback", reqID, model.Alias,
		rawUsage.PromptTokens, rawUsage.CacheCreationInputTokens, rawUsage.CacheReadInputTokens)
	adapterruntime.LogCompleted(d.Log(), ctx, adapterruntime.CompletedAttrs{
		Backend:             "fallback",
		Provider:            "anthropic-fallback",
		Path:                fallback.PathLabel(req),
		SessionID:           req.SessionID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        finishReason,
		TokensIn:            usage.PromptTokens,
		TokensOut:           usage.CompletionTokens,
		CacheReadTokens:     rawUsage.CacheReadInputTokens,
		CacheCreationTokens: rawUsage.CacheCreationInputTokens,
		CacheTTL:            d.CacheTTL(),
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              stream,
	})
}

func logFallbackTerminalCompletion(
	d FallbackResponseDispatcher,
	ctx context.Context,
	req fallback.Request,
	model adaptermodel.ResolvedModel,
	reqID string,
	started time.Time,
	finishReason string,
	usage adapteropenai.Usage,
	rawUsage fallback.Usage,
	stream bool,
) {
	breakdown := adapterruntime.EstimateCost(adapterruntime.CostInputs{
		ModelID:             req.Model,
		TTL:                 d.CacheTTL(),
		InputTokens:         usage.PromptTokens,
		OutputTokens:        usage.CompletionTokens,
		CacheCreationTokens: rawUsage.CacheCreationInputTokens,
		CacheReadTokens:     rawUsage.CacheReadInputTokens,
	})
	d.LogTerminal(ctx, adapterruntime.RequestEvent{
		Stage:               adapterruntime.RequestStageCompleted,
		Provider:            "anthropic-fallback",
		Backend:             model.Backend,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		Stream:              stream,
		FinishReason:        finishReason,
		TokensIn:            usage.PromptTokens,
		TokensOut:           usage.CompletionTokens,
		CacheReadTokens:     rawUsage.CacheReadInputTokens,
		CacheCreationTokens: rawUsage.CacheCreationInputTokens,
		CostMicrocents:      breakdown.TotalMicrocents,
		DurationMs:          time.Since(started).Milliseconds(),
	})
}
