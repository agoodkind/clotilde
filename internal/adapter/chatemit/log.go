package chatemit

import (
	"context"
	"log/slog"
)

type CompletedAttrs struct {
	Backend             string
	RequestID           string
	Alias               string
	ModelID             string
	FinishReason        string
	TokensIn            int
	TokensOut           int
	CacheReadTokens     int
	CacheCreationTokens int
	DurationMs          int64
	Stream              bool

	// Path tags which dispatch leg handled the request so aggregators
	// can compare costs across backends. Known values: "oauth",
	// "fallback_flat" (claude -p with full history in prompt),
	// "fallback_resume" (claude -p --resume against synthesized
	// transcript). Leave empty when the leg cannot be identified.
	Path string
	// SessionID, when set, links log lines from the same conversation
	// across requests. For OAuth this is the adapter-generated
	// request-scoped id; for fallback it is the deterministic session
	// id passed to claude -p.
	SessionID string
	// CacheTTL records the ttl used on cache_control markers ("",
	// "5m", "1h"). Drives the cache-write rate when estimating cost.
	CacheTTL string
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
	breakdown := EstimateCost(CostInputs{
		ModelID:             attrs.ModelID,
		TTL:                 attrs.CacheTTL,
		InputTokens:         attrs.TokensIn,
		OutputTokens:        attrs.TokensOut,
		CacheCreationTokens: attrs.CacheCreationTokens,
		CacheReadTokens:     attrs.CacheReadTokens,
	})
	hitRatio := 0.0
	if denom := attrs.TokensIn + attrs.CacheReadTokens; denom > 0 {
		hitRatio = float64(attrs.CacheReadTokens) / float64(denom)
	}
	args := []slog.Attr{
		slog.String("backend", attrs.Backend),
		slog.String("path", attrs.Path),
		slog.String("session_id", attrs.SessionID),
		slog.String("request_id", attrs.RequestID),
		slog.String("alias", attrs.Alias),
		slog.String("model_id", attrs.ModelID),
		slog.String("finish_reason", attrs.FinishReason),
		slog.Int("tokens_in", attrs.TokensIn),
		slog.Int("tokens_out", attrs.TokensOut),
		slog.Int("cache_read_tokens", attrs.CacheReadTokens),
		slog.Int("cache_creation_tokens", attrs.CacheCreationTokens),
		slog.String("cache_ttl", attrs.CacheTTL),
		slog.Float64("cache_hit_ratio", hitRatio),
		slog.Int64("duration_ms", attrs.DurationMs),
		slog.Bool("stream", attrs.Stream),
		slog.Bool("cost_rates_known", breakdown.RatesKnown),
		slog.Int64("cost_microcents", breakdown.TotalMicrocents),
		slog.Int64("cost_input_microcents", breakdown.InputMicrocents),
		slog.Int64("cost_output_microcents", breakdown.OutputMicrocents),
		slog.Int64("cost_cache_write_microcents", breakdown.CacheWriteMicrocents),
		slog.Int64("cost_cache_read_microcents", breakdown.CacheReadMicrocents),
		slog.Int64("cost_nocache_microcents", breakdown.HypotheticalNoCacheMicrocents),
		slog.Int64("cost_cache_savings_microcents", breakdown.CacheSavingsMicrocents),
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
