package adapter

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

const fineGrainedToolStreamingBeta = "fine-grained-tool-streaming-2025-05-14"

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
	maxTok := anthropicbackend.ResolveMaxTokens(req.MaxTokens, model)
	tr, err := anthropicbackend.TranslateRequest(req, s.anthr.SystemPromptPrefix(), maxTok)
	if err != nil {
		return anthropic.Request{}, err
	}
	// Drop the CLI prefix from tr.System if TranslateRequest already
	// folded it in. It becomes its own typed block below so the cache
	// marker can be stamped on just the prefix without dragging the
	// caller's system text into the cache key.
	prefix := s.anthr.SystemPromptPrefix()
	callerSystem := tr.System
	if strings.HasPrefix(callerSystem, prefix) {
		callerSystem = strings.TrimPrefix(callerSystem, prefix)
		callerSystem = strings.TrimPrefix(callerSystem, "\n\n")
	}
	if instr := jsonSpec.SystemPrompt(false); instr != "" {
		if callerSystem == "" {
			callerSystem = instr
		} else {
			callerSystem = callerSystem + "\n\n" + instr
		}
	}

	// Body-side billing line: required by the upstream identity check.
	cliVersion := anthropic.VersionFromUserAgent(s.anthr.UserAgent())
	if cliVersion == "" {
		cliVersion = s.anthr.CCVersion()
	}
	entry := s.anthr.CCEntrypoint()
	billingHeader := anthropic.BuildAttributionHeader(cliVersion, entry)
	// CLYDE_PROBE_BILLING mutates the billing line for debugging.
	billingHeader = anthropicbackend.MutateBillingForProbe(billingHeader, cliVersion, entry)

	cachingEnabled := true
	if v := s.cfg.ClientIdentity.PromptCachingEnabled; v != nil {
		cachingEnabled = *v
	}
	tr.System = ""
	ttl := strings.TrimSpace(s.cfg.ClientIdentity.PromptCacheTTL)
	if ttl != "" && ttl != "5m" && ttl != "1h" {
		ttl = ""
	}
	scope := strings.TrimSpace(s.cfg.ClientIdentity.PromptCacheScope)
	if scope != "" && scope != "global" && scope != "org" {
		scope = ""
	}
	sysBlocks := anthropicbackend.BuildSystemBlocks(billingHeader, prefix, callerSystem, ttl, scope, cachingEnabled)

	emitToolResultCacheReference := s.cfg.OAuth.ToolResultCacheReferenceEnabled
	strippedModel := anthropicbackend.StripContextSuffix(model.ClaudeModel)
	out, cacheStats := anthropicbackend.ToAPIRequest(tr, strippedModel, emitToolResultCacheReference)
	if cacheStats.ToolResultCandidates > 0 {
		level := slog.LevelInfo
		if emitToolResultCacheReference {
			level = slog.LevelWarn
		}
		s.log.LogAttrs(context.Background(), level, "adapter.cache_breakpoints.tool_result_cache_reference",
			slog.String("component", "adapter"),
			slog.String("subcomponent", "oauth"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", strippedModel),
			slog.Bool("enabled", emitToolResultCacheReference),
			slog.Int("tool_result_candidates", cacheStats.ToolResultCandidates),
			slog.Int("tool_result_applied", cacheStats.ToolResultApplied),
		)
	}
	out.SystemBlocks = sysBlocks

	// Microcompact runs after translation but is logically part of
	// prompt shaping; we do it here (not in toAnthropicAPIRequest) so
	// the config knobs live on the server and the work becomes
	// observable via slog. Must run before applyCacheBreakpoints is
	// re-run in the future, but today applyCacheBreakpoints already
	// fired inside toAnthropicAPIRequest on unmutated content; since
	// it marks the LAST user text (a fresh turn, never a cleared
	// tool_result) the ordering is safe.
	microEnabled := true
	if v := s.cfg.ClientIdentity.MicrocompactEnabled; v != nil {
		microEnabled = *v
	}
	if microEnabled {
		cleared, bytes := anthropicbackend.ApplyMicrocompact(out.Messages, s.cfg.ClientIdentity.MicrocompactKeepRecent)
		anthropicbackend.LogMicrocompact(s.log, "", model.Alias, cleared, bytes, s.cfg.ClientIdentity.MicrocompactKeepRecent)
	}
	// Per-model anthropic-beta extras from configured suffix map.
	out.ExtraBetas = anthropicbackend.DerivePerRequestBetas(model, s.cfg.ClientIdentity.PerContextBetas)
	if req.Stream && len(out.Tools) > 0 {
		out.ExtraBetas = append(out.ExtraBetas, fineGrainedToolStreamingBeta)
	}
	if effort != "" && len(model.Efforts) > 0 {
		out.OutputConfig = &anthropic.OutputConfig{Effort: effort}
	}
	anthropicbackend.ApplyThinkingConfig(&out, model, strippedModel)
	return out, nil
}

// runOAuthTranslatorStream drives tooltrans.StreamTranslator from decoded
// StreamEvents. Both collect and stream paths share this; collect buffers
// chunks while stream writes SSE frames.
func (s *Server) runOAuthTranslatorStream(
	ctx context.Context,
	anthReq anthropic.Request,
	model ResolvedModel,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (anthropic.Usage, string, string, error) {
	return anthropicbackend.RunTranslatorStream(s.anthr, ctx, anthReq, model, reqID, emit)
}

func (s *Server) collectOAuth(w http.ResponseWriter, ctx context.Context, req anthropic.Request, model ResolvedModel, reqID string, started time.Time, jsonSpec JSONResponseSpec, escalate bool, trackerKey string) error {
	var notice *anthropic.Notice
	req.OnHeaders = func(h http.Header) {
		notice = adapterruntime.EvaluateNoticeFromHeaders(h, s.cfg.Notices.EnabledOrDefault(), Claim)
	}
	return anthropicbackend.CollectResponse(s, w, ctx, req, model, reqID, started, jsonSpec, escalate, trackerKey, &notice)
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

// streamOAuth honors the escalate flag for the *initial* call to
// s.anthr.StreamEvents. Once any byte has been written to the SSE stream
// the function commits and never escalates (the response headers
// are already flushed and the dispatcher cannot retry without
// confusing the OpenAI client).
func (s *Server) streamOAuth(w http.ResponseWriter, r *http.Request, req anthropic.Request, model ResolvedModel, reqID string, started time.Time, escalate bool, includeUsage bool, trackerKey string) error {
	var emitTool func(tooltrans.OpenAIStreamChunk) error
	req.OnHeaders = func(h http.Header) {
		if emitTool == nil {
			return
		}
		notice, err := adapterruntime.NoticeForStreamHeaders(
			reqID,
			model.Alias,
			h,
			s.cfg.Notices.EnabledOrDefault(),
			func(chunk tooltrans.OpenAIStreamChunk) error {
				return emitTool(chunk)
			},
			Claim,
			Unclaim,
		)
		if err != nil && notice != nil {
			s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.notice.stream_emit_failed",
				slog.String("request_id", reqID),
				slog.String("alias", model.Alias),
				slog.String("model", req.Model),
				slog.String("kind", notice.Kind),
			)
		}
	}
	return anthropicbackend.StreamResponse(s, w, r, req, model, reqID, started, escalate, includeUsage, trackerKey, &emitTool)
}
