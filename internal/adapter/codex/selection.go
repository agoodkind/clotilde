package codex

import (
    "context"
    "fmt"
    "log/slog"
    "time"

    adaptermodel "goodkind.io/clyde/internal/adapter/model"
    adapteropenai "goodkind.io/clyde/internal/adapter/openai"
    adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
    "goodkind.io/clyde/internal/adapter/tooltrans"
)

type transportSelector interface {
    AppFallbackEnabled() bool
    Log() *slog.Logger
    LogTerminal(context.Context, adapterruntime.RequestEvent)
}

type fallbackRunFunc func() (any, bool, error)

func resolveTransportSelection(
    d transportSelector,
    ctx context.Context,
    req adapteropenai.ChatRequest,
    model adaptermodel.ResolvedModel,
    reqID string,
    started time.Time,
    directChunks []tooltrans.OpenAIStreamChunk,
    directRes any,
    directErr error,
    stream bool,
    fallback fallbackRunFunc,
) (path string, res any, managed bool, err error) {
    path = "direct"
    res = directRes
    err = directErr

    if err == nil && d.AppFallbackEnabled() {
        finishReason := ""
        if runResult, ok := res.(RunResult); ok {
            finishReason = runResult.FinishReason
        }
        if escalate, reason := ShouldEscalateDirect(req, directChunks, finishReason); escalate {
            d.Log().LogAttrs(ctx, slog.LevelWarn, "adapter.codex.direct.degraded",
                slog.String("request_id", reqID),
                slog.String("reason", reason),
                slog.String("alias", model.Alias),
                slog.String("model", model.ClaudeModel),
            )
            err = fmt.Errorf("codex direct degraded: %s", reason)
        }
    }
    if err != nil && d.AppFallbackEnabled() {
        d.LogTerminal(ctx, adapterruntime.RequestEvent{
            Stage:      adapterruntime.RequestStageFailed,
            Provider:   providerName(model, "direct"),
            Backend:    model.Backend,
            RequestID:  reqID,
            Alias:      model.Alias,
            ModelID:    model.Alias,
            Stream:     stream,
            DurationMs: time.Since(started).Milliseconds(),
            Err:        err.Error(),
        })
        d.Log().LogAttrs(ctx, slog.LevelWarn, "adapter.codex.fallback.escalating",
            slog.String("request_id", reqID),
            slog.Any("err", err),
        )
        path = "app"
        res, managed, err = fallback()
    }
    return path, res, managed, err
}
