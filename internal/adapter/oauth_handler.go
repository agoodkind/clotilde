package adapter

import (
	"context"
	"log/slog"
	"net/http"

	"goodkind.io/clyde/internal/adapter/anthropic"
	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
)

// handleOAuth fulfils a chat completion using the direct HTTP
// anthropic.Client (Bearer token from the oauth manager). Streaming
// and non-streaming responses mirror the fallback CLI path shape for
// OpenAI-compatible clients.
//
// When escalate is true the function returns a non-nil error
// without writing the response on transport failures, letting the
// dispatcher trigger the fallback. When escalate is false the
// function writes the error to w (preserving the original behavior)
// and returns nil.
func (s *Server) handleOAuth(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, escalate bool) error {
	return s.HandleOAuth(w, r, req, model, effort, reqID, escalate)
}

// buildAnthropicWire maps the OpenAI chat request to a native messages body
// via tooltrans, then applies thinking and effort knobs that are not part of
// the OpenAI wire shape.
func (s *Server) buildAnthropicWire(req ChatRequest, model ResolvedModel, effort string, jsonSpec JSONResponseSpec, reqID string) (anthropic.Request, error) {
	return anthropicbackend.BuildRequest(context.Background(), req, model, effort, anthropicbackend.BuildRequestConfig{
		SystemPromptPrefix:              s.anthr.SystemPromptPrefix(),
		UserAgent:                       s.anthr.UserAgent(),
		CCVersion:                       s.anthr.CCVersion(),
		CCEntrypoint:                    s.anthr.CCEntrypoint(),
		JSONSystemPrompt:                jsonSpec.SystemPrompt(false),
		PromptCachingEnabled:            s.cfg.ClientIdentity.PromptCachingEnabled,
		PromptCacheTTL:                  s.cfg.ClientIdentity.PromptCacheTTL,
		PromptCacheScope:                s.cfg.ClientIdentity.PromptCacheScope,
		ToolResultCacheReferenceEnabled: s.cfg.OAuth.ToolResultCacheReferenceEnabled,
		MicrocompactEnabled:             s.cfg.ClientIdentity.MicrocompactEnabled,
		MicrocompactKeepRecent:          s.cfg.ClientIdentity.MicrocompactKeepRecent,
		PerContextBetas:                 s.cfg.ClientIdentity.PerContextBetas,
		Logger:                          s.log,
	}, reqID)
}

// logCacheUsage emits a dedicated adapter.cache.usage slog event when
// the upstream reports any cache activity. The hit_ratio denominator
// is input_tokens + cache_read_input_tokens since Anthropic bills
// input_tokens as the uncached portion only; a value of 1.0 means the
// entire prompt came from cache. Callers on the OAuth path pass the
// native anthropic.Usage via logCacheUsageAnthropic; the fallback path
// passes the fields it already parsed from stream-json result events.
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

func (s *Server) logCacheUsageAnthropic(ctx context.Context, backend, reqID, alias string, u anthropic.Usage) {
	s.logCacheUsage(ctx, backend, reqID, alias, u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens)
}
