package adapter

import (
	"context"
	"log/slog"

	"goodkind.io/clyde/internal/adapter/anthropic"
)

// logCacheUsage emits a dedicated adapter.cache.usage slog event when
// the upstream reports any cache activity. The hit_ratio denominator
// is input_tokens + cache_read_input_tokens since Anthropic bills
// input_tokens as the uncached portion only; a value of 1.0 means the
// entire prompt came from cache.
func (s *Server) logCacheUsage(ctx context.Context, backend, reqID, alias string, inputTokens, cacheCreationTokens, cacheReadTokens int) {
	if cacheCreationTokens == 0 && cacheReadTokens == 0 {
		return
	}
	denom := inputTokens + cacheReadTokens
	var hitRatio float64
	if denom > 0 {
		hitRatio = float64(cacheReadTokens) / float64(denom)
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.cache.usage",
		slog.String("backend", backend),
		slog.String("request_id", reqID),
		slog.String("alias", alias),
		slog.Int("input_tokens", inputTokens),
		slog.Int("cache_creation_tokens", cacheCreationTokens),
		slog.Int("cache_read_tokens", cacheReadTokens),
		slog.Float64("hit_ratio", hitRatio),
	)
}

func (s *Server) logCacheUsageAnthropic(ctx context.Context, backend, reqID, alias string, usage anthropic.Usage) {
	s.logCacheUsage(ctx, backend, reqID, alias, usage.InputTokens, usage.CacheCreationInputTokens, usage.CacheReadInputTokens)
}
