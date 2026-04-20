package chatemit

import (
	"context"
	"log/slog"
)

type CompletedAttrs struct {
	Backend      string
	RequestID    string
	Alias        string
	ModelID      string
	FinishReason string
	TokensIn     int
	TokensOut    int
	DurationMs   int64
	Stream       bool
}

// LogCompleted emits a normalized adapter chat completion log with model_id and
// a one-cycle legacy model key for backward compatibility.
func LogCompleted(log *slog.Logger, ctx context.Context, attrs CompletedAttrs) {
	if log == nil {
		return
	}
	if attrs.Backend == "" {
		attrs.Backend = "unknown"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	args := []slog.Attr{
		slog.String("backend", attrs.Backend),
		slog.String("request_id", attrs.RequestID),
		slog.String("alias", attrs.Alias),
		slog.String("model_id", attrs.ModelID),
		slog.String("finish_reason", attrs.FinishReason),
		slog.Int("tokens_in", attrs.TokensIn),
		slog.Int("tokens_out", attrs.TokensOut),
		slog.Int64("duration_ms", attrs.DurationMs),
		slog.Bool("stream", attrs.Stream),
	}
	switch attrs.Backend {
	case "anthropic":
		args = append(args, slog.String("model", attrs.ModelID))
	case "fallback":
		args = append(args, slog.String("cli_model", attrs.ModelID))
	default:
		args = append(args, slog.String("model", attrs.ModelID))
	}
	log.LogAttrs(ctx, slog.LevelInfo, "adapter.chat.completed", args...)
}

type FailedAttrs struct {
	Backend    string
	RequestID  string
	Alias      string
	ModelID    string
	Err        error
	DurationMs int64
}

// LogFailed emits shared failure metadata for chat handlers.
func LogFailed(log *slog.Logger, ctx context.Context, attrs FailedAttrs) {
	if log == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	log.LogAttrs(ctx, slog.LevelError, "adapter.chat.failed", []slog.Attr{
		slog.String("backend", attrs.Backend),
		slog.String("request_id", attrs.RequestID),
		slog.String("alias", attrs.Alias),
		slog.String("model", attrs.ModelID),
		slog.Int64("duration_ms", attrs.DurationMs),
		slog.Any("err", attrs.Err),
	}...)
}
