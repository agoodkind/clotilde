package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/chatemit"
	"goodkind.io/clyde/internal/adapter/tooltrans"
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
	if s.anthr == nil {
		if err := chatemit.EscalateOrWrite(
			fmt.Errorf("oauth_unconfigured: adapter built without anthropic client"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"oauth_unconfigured",
			"adapter built without anthropic client; set adapter.direct_oauth=true and restart",
		); err != nil {
			return err
		}
		return nil
	}
	if err := s.acquire(r.Context()); err != nil {
		if err2 := chatemit.EscalateOrWrite(
			fmt.Errorf("rate_limited: %w", err),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusTooManyRequests,
			"rate_limited",
			err.Error(),
		); err2 != nil {
			return err2
		}
		return nil
	}
	defer s.release()

	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	trackerKey := requestContextTrackerKey(req, model.Alias)
	anthReq, err := s.buildAnthropicWire(req, model, effort, jsonSpec, reqID)
	if err != nil {
		if err2 := chatemit.EscalateOrWrite(
			fmt.Errorf("oauth_translate: %w", err),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadRequest,
			"invalid_request",
			err.Error(),
		); err2 != nil {
			return err2
		}
		return nil
	}

	started := time.Now()
	if req.Stream {
		// Always emit the final usage chunk; many OpenAI-compat clients
		// (Cursor, etc.) read per-turn token counts from it without setting
		// stream_options.include_usage.
		_ = req.StreamOptions
		return s.streamOAuth(w, r, anthReq, model, reqID, started, escalate, true, trackerKey)
	}
	return s.collectOAuth(w, r.Context(), anthReq, model, reqID, started, jsonSpec, escalate, trackerKey)
}

// buildAnthropicWire maps the OpenAI chat request to a native messages body
// via tooltrans, then applies thinking and effort knobs that are not part of
// the OpenAI wire shape.
func (s *Server) buildAnthropicWire(req ChatRequest, model ResolvedModel, effort string, jsonSpec JSONResponseSpec, reqID string) (anthropic.Request, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return anthropic.Request{}, err
	}
	var oaReq tooltrans.OpenAIRequest
	if err := json.Unmarshal(raw, &oaReq); err != nil {
		return anthropic.Request{}, err
	}
	maxTok := anthropicMaxTokens(req.MaxTokens)
	if model.MaxOutputTokens > 0 && maxTok > model.MaxOutputTokens {
		maxTok = model.MaxOutputTokens
	}
	tr, err := tooltrans.TranslateRequest(oaReq, s.anthr.SystemPromptPrefix(), maxTok)
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
	billingHeader = mutateBillingForProbe(billingHeader, cliVersion, entry)

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
	sysBlocks := buildSystemBlocks(billingHeader, prefix, callerSystem, ttl, scope, cachingEnabled)

	emitToolResultCacheReference := s.cfg.OAuth.ToolResultCacheReferenceEnabled
	out, cacheStats := toAnthropicAPIRequest(tr, stripContextSuffix(model.ClaudeModel), emitToolResultCacheReference)
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
			slog.String("model", stripContextSuffix(model.ClaudeModel)),
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
		cleared, bytes := applyMicrocompact(out.Messages, s.cfg.ClientIdentity.MicrocompactKeepRecent)
		logMicrocompact(s.log, "", model.Alias, cleared, bytes, s.cfg.ClientIdentity.MicrocompactKeepRecent)
	}
	// Per-model anthropic-beta extras from configured suffix map.
	out.ExtraBetas = derivePerRequestBetas(model, s.cfg.ClientIdentity.PerContextBetas)
	if effort != "" && len(model.Efforts) > 0 {
		out.OutputConfig = &anthropic.OutputConfig{Effort: effort}
	}
	switch effectiveThinkingMode(model) {
	case ThinkingAdaptive:
		out.Thinking = &anthropic.Thinking{
			Type:    "adaptive",
			Display: "summarized",
		}
	case ThinkingEnabled:
		// Mirror the canonical Claude Code CLI formula. See
		// claude-code-source-code-full/src/services/api/claude.ts:1617-1628
		// and src/utils/context.ts:219 (getMaxThinkingTokensForModel
		// returns upperLimit - 1). The CLI sends max_tokens equal to
		// the model's full output cap and budget_tokens equal to
		// max_tokens - 1, letting the model self-limit thinking.
		//
		// When MaxOutputTokens is populated in the registry (the
		// common case, set per family in the toml) we override the
		// caller's max_tokens so the model has real room to both
		// think and answer. The OpenAI response still reports the
		// true completion_tokens from Anthropic, so the caller's UI
		// sees the honest post-turn cost.
		//
		// When MaxOutputTokens is zero (misconfigured family) we
		// fall back to the caller's max_tokens and derive the
		// budget from it, floored at Anthropic's documented 1024
		// minimum for thinking. This stays data-driven and never
		// hardcodes a model identifier.
		cap := model.MaxOutputTokens
		if cap <= 0 {
			cap = out.MaxTokens
		}
		if cap < 1025 {
			// Anthropic thinking requires budget_tokens >= 1024 and
			// max_tokens > budget_tokens. Promote both to the
			// smallest pair that satisfies both constraints.
			cap = 1025
		}
		out.MaxTokens = cap
		out.Thinking = &anthropic.Thinking{
			Type:         "enabled",
			BudgetTokens: cap - 1,
			Display:      "summarized",
		}
	case ThinkingDisabled:
		out.Thinking = &anthropic.Thinking{Type: "disabled"}
	}
	return out, nil
}

func effectiveThinkingMode(model ResolvedModel) string {
	// Claude Opus 4.7 only supports adaptive thinking upstream. Keep the
	// `...-thinking-enabled` aliases for compatibility, but normalize them
	// before we hit Anthropic so the request shape matches the current API.
	if strings.EqualFold(stripContextSuffix(model.ClaudeModel), "claude-opus-4-7") &&
		model.Thinking == ThinkingEnabled {
		return ThinkingAdaptive
	}
	return model.Thinking
}

func toAnthropicAPIRequest(tr tooltrans.AnthRequest, claudeModel string, emitToolResultCacheReference bool) (anthropic.Request, cacheBreakpointStats) {
	msgs := make([]anthropic.Message, 0, len(tr.Messages))
	for _, m := range tr.Messages {
		blocks := make([]anthropic.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			var src *anthropic.ImageSource
			if b.Source != nil {
				src = &anthropic.ImageSource{
					Type:      b.Source.Type,
					MediaType: b.Source.MediaType,
					Data:      b.Source.Data,
					URL:       b.Source.URL,
				}
			}
			blocks = append(blocks, anthropic.ContentBlock{
				Type:      b.Type,
				Text:      b.Text,
				ID:        b.ID,
				Name:      b.Name,
				Input:     b.Input,
				ToolUseID: b.ToolUseID,
				Content:   b.ResultContent,
				Source:    src,
			})
		}
		msgs = append(msgs, anthropic.Message{Role: m.Role, Content: blocks})
	}
	tools := make([]anthropic.Tool, 0, len(tr.Tools))
	for _, t := range tr.Tools {
		tools = append(tools, anthropic.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	var tc *anthropic.ToolChoice
	if tr.ToolChoice != nil {
		tc = &anthropic.ToolChoice{
			Type:                   tr.ToolChoice.Type,
			Name:                   tr.ToolChoice.Name,
			DisableParallelToolUse: tr.ToolChoice.DisableParallelToolUse,
		}
	}
	stats := applyCacheBreakpoints(msgs, tools, emitToolResultCacheReference)
	return anthropic.Request{
		Model:      claudeModel,
		System:     tr.System,
		Messages:   msgs,
		MaxTokens:  tr.MaxTokens,
		Stream:     false,
		Tools:      tools,
		ToolChoice: tc,
	}, stats
}

// buildSystemBlocks assembles the typed system-prompt array with
// cache_control markers matching what the live CLI sends. Order:
//
//	0: billing attribution line (no cache_control; the cch component
//	   varies per request so it cannot share a cache key with
//	   subsequent turns).
//	1: CLI system prompt prefix (ephemeral 1h; stable across all
//	   requests for the session lifetime).
//	2: caller-supplied system text plus any JSON-mode instruction
//	   (ephemeral 1h; stable while the client reuses the same system).
//
// Empty inputs are skipped so the wire never carries zero-length
// blocks. When cachingEnabled is false all markers are omitted and
// the blocks ship plain; the array form still communicates the three
// logical segments to the server, which accepts either shape.
func buildSystemBlocks(billing, prefix, callerSystem, ttl, scope string, cachingEnabled bool) []anthropic.SystemBlock {
	// Scope applies only to the CLI-prefix block. The caller-system
	// block stays session-scoped because its content varies per
	// caller and does not benefit from a global key. Mirrors
	// splitSysPromptPrefix in src/utils/api.ts:321 where the static
	// CLI prefix gets cacheScope='global' and dynamic blocks get
	// cacheScope=null.
	var cacheMarker *anthropic.CacheControl
	var prefixMarker *anthropic.CacheControl
	if cachingEnabled {
		cacheMarker = &anthropic.CacheControl{Type: "ephemeral", TTL: ttl}
		prefixMarker = &anthropic.CacheControl{Type: "ephemeral", TTL: ttl, Scope: scope}
	}
	var out []anthropic.SystemBlock
	if strings.TrimSpace(billing) != "" {
		out = append(out, anthropic.SystemBlock{
			Type: "text",
			Text: billing,
			// Billing line is intentionally uncached: cch varies per
			// request, so caching would produce immediate misses and
			// waste a marker slot. Mirrors CLI behavior at
			// src/services/api/claude.ts:3230 (cacheScope: null).
		})
	}
	if strings.TrimSpace(prefix) != "" {
		out = append(out, anthropic.SystemBlock{
			Type:         "text",
			Text:         prefix,
			CacheControl: prefixMarker,
		})
	}
	if strings.TrimSpace(callerSystem) != "" {
		out = append(out, anthropic.SystemBlock{
			Type:         "text",
			Text:         callerSystem,
			CacheControl: cacheMarker,
		})
	}
	return out
}

type cacheBreakpointStats struct {
	ToolResultCandidates int
	ToolResultApplied    int
}

// applyCacheBreakpoints stamps the message-level cache_control marker matching
// the live Claude CLI. tool_result.cache_reference is separately gated because
// the direct Anthropic OAuth /v1/messages tool-followup path rejected it in
// production, while official Claude CLI MITM captures on successful tool turns
// did not include it. Keep the knob for controlled upstream experiments rather
// than assuming every Claude transport accepts the cached-MC shape.
func applyCacheBreakpoints(msgs []anthropic.Message, tools []anthropic.Tool, emitToolResultCacheReference bool) cacheBreakpointStats {
	var stats cacheBreakpointStats
	ephemeral := &anthropic.CacheControl{Type: "ephemeral"}
	if len(tools) > 0 {
		tools[len(tools)-1].CacheControl = ephemeral
	}
	if len(msgs) == 0 {
		return stats
	}
	lastCCMsg := -1
	markerIndex := len(msgs) - 1
	msg := &msgs[markerIndex]
	for j := len(msg.Content) - 1; j >= 0; j-- {
		if !cacheableMessageBoundaryBlock(msg.Role, msg.Content[j].Type) {
			continue
		}
		msg.Content[j].CacheControl = ephemeral
		lastCCMsg = markerIndex
		break
	}
	if lastCCMsg < 0 {
		return stats
	}
	for i := 0; i < lastCCMsg; i++ {
		if msgs[i].Role != "user" {
			continue
		}
		for j := range msgs[i].Content {
			block := &msgs[i].Content[j]
			if block.Type != "tool_result" || strings.TrimSpace(block.ToolUseID) == "" {
				continue
			}
			stats.ToolResultCandidates++
			if !emitToolResultCacheReference {
				continue
			}
			block.CacheReference = block.ToolUseID
			stats.ToolResultApplied++
		}
	}
	return stats
}

func cacheableMessageBoundaryBlock(role, blockType string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		switch blockType {
		case "thinking", "redacted_thinking", "connector_text":
			return false
		default:
			return true
		}
	default:
		return true
	}
}

// streamEventToTranslatorSSE maps decoded native stream signals to the JSON
// payloads tooltrans.StreamTranslator.HandleEvent expects for SSE event names
// (content_block_start, content_block_delta, content_block_stop). Raw SSE is
// decoded first; this layer re-encodes the subset the translator consumes.
func streamEventToTranslatorSSE(ev anthropic.StreamEvent) (eventName string, payload []byte, ok bool) {
	switch ev.Kind {
	case "text":
		p := struct {
			Index int `json:"index"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}{
			Index: ev.BlockIndex,
			Delta: struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text_delta", Text: ev.Text},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_delta", b, true
	case "tool_use_start":
		p := struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}{
			Index: ev.BlockIndex,
			ContentBlock: struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}{Type: "tool_use", ID: ev.ToolUseID, Name: ev.ToolUseName},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_start", b, true
	case "tool_use_arg_delta":
		p := struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}{
			Index: ev.BlockIndex,
			Delta: struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			}{Type: "input_json_delta", PartialJSON: ev.PartialJSON},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_delta", b, true
	case "tool_use_stop":
		p := struct {
			Index int `json:"index"`
		}{Index: ev.BlockIndex}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_stop", b, true
	case "thinking":
		if ev.Text != "" {
			p := struct {
				Index int `json:"index"`
				Delta struct {
					Type     string `json:"type"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			}{
				Index: ev.BlockIndex,
				Delta: struct {
					Type     string `json:"type"`
					Thinking string `json:"thinking"`
				}{Type: "thinking_delta", Thinking: ev.Text},
			}
			b, err := json.Marshal(p)
			if err != nil {
				return "", nil, false
			}
			return "content_block_delta", b, true
		}
		p := struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}{
			Index: ev.BlockIndex,
			ContentBlock: struct {
				Type string `json:"type"`
			}{Type: "thinking"},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_start", b, true
	default:
		return "", nil, false
	}
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
	tr := tooltrans.NewStreamTranslator(reqID, model.Alias)
	msgStartPayload, err := json.Marshal(struct {
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}{})
	if err != nil {
		return anthropic.Usage{}, "", "", err
	}
	msgStartChunks, _, _, _, err := tr.HandleEvent("message_start", msgStartPayload)
	if err != nil {
		return anthropic.Usage{}, "", "", err
	}
	for _, ch := range msgStartChunks {
		if err := emit(ch); err != nil {
			return anthropic.Usage{}, "", "", err
		}
	}

	var streamStopReason string
	anthUsage, _, err := s.anthr.StreamEvents(ctx, anthReq, func(ev anthropic.StreamEvent) error {
		if ev.Kind == "stop" {
			streamStopReason = ev.StopReason
			return nil
		}
		evName, payload, ok := streamEventToTranslatorSSE(ev)
		if !ok {
			return nil
		}
		outChunks, _, _, _, handleErr := tr.HandleEvent(evName, payload)
		if handleErr != nil {
			return handleErr
		}
		for _, ch := range outChunks {
			if err := emit(ch); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}

	mdPayload, err := json.Marshal(struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}{
		Delta: struct {
			StopReason string `json:"stop_reason"`
		}{StopReason: streamStopReason},
		Usage: struct {
			OutputTokens int `json:"output_tokens"`
		}{OutputTokens: anthUsage.OutputTokens},
	})
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	mdChunks, _, _, _, err := tr.HandleEvent("message_delta", mdPayload)
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	for _, ch := range mdChunks {
		if err := emit(ch); err != nil {
			return anthUsage, streamStopReason, "", err
		}
	}

	stopChunks, _, finishReason, _, err := tr.HandleEvent("message_stop", []byte("{}"))
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	for _, ch := range stopChunks {
		if err := emit(ch); err != nil {
			return anthUsage, streamStopReason, "", err
		}
	}
	return anthUsage, streamStopReason, finishReason, nil
}

func (s *Server) collectOAuth(w http.ResponseWriter, ctx context.Context, req anthropic.Request, model ResolvedModel, reqID string, started time.Time, jsonSpec JSONResponseSpec, escalate bool, trackerKey string) error {
	s.emitRequestStarted(ctx, model, "oauth", reqID, req.Model, false)
	var buf []tooltrans.OpenAIStreamChunk
	var notice *anthropic.Notice
	emit := func(ch tooltrans.OpenAIStreamChunk) error {
		buf = append(buf, ch)
		return nil
	}
	req.OnHeaders = func(h http.Header) {
		notice = chatemit.EvaluateNoticeFromHeaders(h, s.cfg.Notices.EnabledOrDefault(), Claim)
	}
	anthUsage, anthStopReason, finishReason, err := s.runOAuthTranslatorStream(ctx, req, model, reqID, emit)
	if err != nil {
		chatemit.LogFailed(s.log, ctx, chatemit.FailedAttrs{
			Backend:    "anthropic",
			Provider:   providerName(model, "oauth"),
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Err:        err,
			DurationMs: time.Since(started).Milliseconds(),
		})
		chatemit.LogTerminal(s.log, ctx, s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   providerName(model, "oauth"),
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
				// We are about to retry on another backend; release the
				// notice slot so a successful retry can still deliver it.
				Unclaim(notice.Kind, notice.ResetsAt)
				s.log.LogAttrs(ctx, slog.LevelDebug, "adapter.notice.unclaimed_on_escalate",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", notice.Kind),
				)
			} else {
				errMsg = errMsg + " · " + notice.Text
				s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.notice.injected_into_error",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", notice.Kind),
					slog.String("notice_text", notice.Text),
				)
			}
		}
		if err := chatemit.EscalateOrWrite(
			err,
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadGateway,
			"upstream_error",
			errMsg,
		); err != nil {
			return err
		}
		return nil
	}
	rawUsage := usageFromAnthropic(anthUsage)
	tracked := s.ctxUsage.Track(trackerKey, rawUsage)
	u := tracked.usage
	resp := mergeOAuthStreamChunks(reqID, model.Alias, buf, u, finishReason, jsonSpec, anthStopReason)
	resp, _ = chatemit.NoticeForResponseHeaders(resp, notice, Unclaim, json.Marshal)
	writeJSON(w, http.StatusOK, resp)
	if u.PromptTokens != rawUsage.PromptTokens || u.TotalTokens != rawUsage.TotalTokens {
		s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.context_usage.tracked",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.Int("raw_prompt_tokens", tracked.rawPrompt),
			slog.Int("raw_total_tokens", tracked.rawTotal),
			slog.Int("rolled_output_tokens", tracked.rolledFrom),
			slog.Int("surfaced_prompt_tokens", u.PromptTokens),
			slog.Int("surfaced_total_tokens", u.TotalTokens),
		)
	}
	s.logCacheUsageAnthropic(ctx, "anthropic", reqID, model.Alias, anthUsage)
	chatemit.LogCompleted(s.log, ctx, chatemit.CompletedAttrs{
		Backend:             "anthropic",
		Provider:            providerName(model, "oauth"),
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
		CacheTTL:            s.cfg.ClientIdentity.PromptCacheTTL,
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              false,
	})
	breakdown := chatemit.EstimateCost(chatemit.CostInputs{
		ModelID:             req.Model,
		TTL:                 s.cfg.ClientIdentity.PromptCacheTTL,
		InputTokens:         u.PromptTokens,
		OutputTokens:        u.CompletionTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
	})
	chatemit.LogTerminal(s.log, ctx, s.deps.RequestEvents, chatemit.RequestEvent{
		Stage:               chatemit.RequestStageCompleted,
		Provider:            providerName(model, "oauth"),
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

// usageFromAnthropic converts an upstream usage block into the OpenAI
// shape. Cache-read tokens surface in prompt_tokens_details.cached_tokens
// so OpenAI clients that display the breakdown see cache efficiency;
// cache-creation tokens have no OpenAI-canonical field and are only
// reported via slog.
// usageFromAnthropic converts an upstream usage block into the
// OpenAI response shape. OpenAI's convention is that prompt_tokens
// represents the total input sent (including cached and
// cache-created tokens), and prompt_tokens_details.cached_tokens is
// a subset-of breakdown. Anthropic's native shape splits them:
// input_tokens is only the uncached portion, and cache_read /
// cache_creation are separate counters. Clients like Cursor compute
// "context used" from prompt_tokens + completion_tokens, so if we
// only forward Anthropic's input_tokens the UI undercounts by tens
// of thousands of tokens the moment caching kicks in. Sum the three
// Anthropic counters into a single OpenAI prompt_tokens to match
// what Cursor expects; prompt_tokens_details.cached_tokens still
// exposes the cache-read breakdown for tools that know to read it.
func usageFromAnthropic(a anthropic.Usage) Usage {
	totalInput := a.InputTokens + a.CacheReadInputTokens + a.CacheCreationInputTokens
	u := Usage{
		PromptTokens:     totalInput,
		CompletionTokens: a.OutputTokens,
		TotalTokens:      totalInput + a.OutputTokens,
	}
	if a.CacheReadInputTokens > 0 {
		u.PromptTokensDetails = &PromptTokensDetails{CachedTokens: a.CacheReadInputTokens}
	}
	return u
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
	s.emitRequestStarted(r.Context(), model, "oauth", reqID, req.Model, true)
	sw, err := newSSEWriter(w)
	if err != nil {
		if err := chatemit.EscalateOrWrite(
			fmt.Errorf("no_flusher: streaming not supported by transport"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"no_flusher",
			err.Error(),
		); err != nil {
			return err
		}
		return nil
	}

	// Flush SSE headers immediately so clients (e.g. Cursor) get a
	// response committal before we wait for the upstream's first byte.
	// Large prompts spend ~1-3s on TTFT; without an early flush, strict
	// streaming clients close the connection on timeout.
	sw.writeSSEHeaders()
	s.emitRequestStreamOpened(r.Context(), model, "oauth", reqID, req.Model, true)

	emittedContent := false
	emit := func(chunk StreamChunk) error {
		if streamChunkHasVisibleContent(chunk) {
			emittedContent = true
		}
		return sw.emitStreamChunk(systemFingerprint, chunk)
	}

	emitTool := func(och tooltrans.OpenAIStreamChunk) error {
		return emit(streamChunkFromTooltrans(och))
	}
	req.OnHeaders = func(h http.Header) {
		notice, err := chatemit.NoticeForStreamHeaders(
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

	anthUsage, _, finishReason, err := s.runOAuthTranslatorStream(r.Context(), req, model, reqID, func(ch tooltrans.OpenAIStreamChunk) error {
		return emitTool(ch)
	})
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.chat.stream_error",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.Any("err", err),
		)
		if escalate && !sw.hasCommittedHeaders() {
			return err
		}
		if !emittedContent {
			_ = emitActionableStreamError(emit, reqID, model.Alias, err)
			finishReason = "stop"
		}
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   providerName(model, "oauth"),
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
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.chat.stream_empty",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.Any("err", streamErr),
		)
		_ = emit(StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model.Alias,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: StreamDelta{
					Role:    "assistant",
					Content: "Clyde adapter upstream stream ended before producing content. Claude authentication may be invalid. Run `claude /login`, then retry.",
				},
			}},
		})
		finishReason = "stop"
	}

	fr := finishReason
	_ = chatemit.EmitFinishChunk(emit, reqID, model.Alias, time.Now().Unix(), fr)

	rawFinalUsage := usageFromAnthropic(anthUsage)
	tracked := s.ctxUsage.Track(trackerKey, rawFinalUsage)
	finalUsage := tracked.usage
	if err == nil && emittedContent && finalUsage.PromptTokens == 0 && finalUsage.CompletionTokens == 0 {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.anthropic.usage_missing",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.String("stop_reason", fr),
			slog.Int("raw_input_tokens", anthUsage.InputTokens),
			slog.Int("raw_output_tokens", anthUsage.OutputTokens),
			slog.Int("raw_cache_read_tokens", anthUsage.CacheReadInputTokens),
			slog.Int("raw_cache_creation_tokens", anthUsage.CacheCreationInputTokens),
		)
	}
	if finalUsage.PromptTokens != rawFinalUsage.PromptTokens || finalUsage.TotalTokens != rawFinalUsage.TotalTokens {
		s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.context_usage.tracked",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.Int("raw_prompt_tokens", tracked.rawPrompt),
			slog.Int("raw_total_tokens", tracked.rawTotal),
			slog.Int("rolled_output_tokens", tracked.rolledFrom),
			slog.Int("surfaced_prompt_tokens", finalUsage.PromptTokens),
			slog.Int("surfaced_total_tokens", finalUsage.TotalTokens),
		)
	}
	if includeUsage {
		_ = chatemit.EmitUsageChunk(emit, reqID, model.Alias, time.Now().Unix(), finalUsage)
	}
	_ = sw.writeStreamDone()

	s.logCacheUsageAnthropic(r.Context(), "anthropic", reqID, model.Alias, anthUsage)
	chatemit.LogCompleted(s.log, r.Context(), chatemit.CompletedAttrs{
		Backend:             "anthropic",
		Provider:            providerName(model, "oauth"),
		Path:                "oauth",
		SessionID:           reqID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        fr,
		TokensIn:            finalUsage.PromptTokens,
		TokensOut:           finalUsage.CompletionTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheTTL:            s.cfg.ClientIdentity.PromptCacheTTL,
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              true,
	})
	breakdown := chatemit.EstimateCost(chatemit.CostInputs{
		ModelID:             req.Model,
		TTL:                 s.cfg.ClientIdentity.PromptCacheTTL,
		InputTokens:         finalUsage.PromptTokens,
		OutputTokens:        finalUsage.CompletionTokens,
		CacheCreationTokens: anthUsage.CacheCreationInputTokens,
		CacheReadTokens:     anthUsage.CacheReadInputTokens,
	})
	if err == nil {
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:               chatemit.RequestStageCompleted,
			Provider:            providerName(model, "oauth"),
			Backend:             model.Backend,
			RequestID:           reqID,
			Alias:               model.Alias,
			ModelID:             req.Model,
			Stream:              true,
			FinishReason:        fr,
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

// mutateBillingForProbe applies CLYDE_PROBE_BILLING for debugging.
// canonical includes cc_version, cc_entrypoint, and cch. Returns ""
// to omit the line entirely.
func mutateBillingForProbe(canonical, cliVersion, ccEntrypoint string) string {
	mode := strings.TrimSpace(os.Getenv("CLYDE_PROBE_BILLING"))
	if mode == "" {
		return canonical
	}
	const prefix = "x-anthropic-billing-header: "
	switch mode {
	case "omit":
		return ""
	case "wrong_fp":
		return prefix + "cc_version=" + cliVersion + ".zzz; cc_entrypoint=" + ccEntrypoint + "; cch=00000;"
	case "omit_fp":
		return prefix + "cc_version=" + cliVersion + "; cc_entrypoint=" + ccEntrypoint + "; cch=00000;"
	case "bad_entrypoint":
		fp := extractFingerprint(canonical)
		cchVal := extractBillingCCH(canonical)
		if cchVal == "" {
			cchVal = "00000"
		}
		return prefix + "cc_version=" + cliVersion + "." + fp + "; cc_entrypoint=garbage; cch=" + cchVal + ";"
	case "omit_entrypoint":
		fp := extractFingerprint(canonical)
		cchVal := extractBillingCCH(canonical)
		if cchVal == "" {
			cchVal = "00000"
		}
		return prefix + "cc_version=" + cliVersion + "." + fp + "; cch=" + cchVal + ";"
	case "cch_zero":
		return replaceBillingCCH(canonical, "00000")
	case "cch_z":
		return replaceBillingCCH(canonical, "ZZZZZ")
	case "cch_long":
		return replaceBillingCCH(canonical, strings.Repeat("a", 32))
	default:
		// Unknown mode: ship canonical so a typo doesn't silently
		// drop the bucket signal.
		return canonical
	}
}

// replaceBillingCCH swaps the value after `cch=` up to the next `;`.
func replaceBillingCCH(line, newVal string) string {
	const marker = "cch="
	before, after, ok := strings.Cut(line, marker)
	if !ok {
		return line + " cch=" + newVal + ";"
	}
	_, tail, ok2 := strings.Cut(after, ";")
	if !ok2 {
		return before + marker + newVal
	}
	return before + marker + newVal + ";" + tail
}

// extractBillingCCH returns the cch hex token or "" if absent.
func extractBillingCCH(line string) string {
	const marker = "cch="
	_, after, ok := strings.Cut(line, marker)
	if !ok {
		return ""
	}
	val, _, _ := strings.Cut(after, ";")
	return val
}

// extractFingerprint returns the 3-char fp suffix from a canonical
// billing line. Tolerates absence by returning "".
func extractFingerprint(line string) string {
	const verPrefix = "cc_version="
	_, rest, ok := strings.Cut(line, verPrefix)
	if !ok {
		return ""
	}
	verPart, _, ok2 := strings.Cut(rest, ";")
	if !ok2 {
		return ""
	}
	dot := strings.LastIndexByte(verPart, '.')
	if dot < 0 {
		return ""
	}
	return verPart[dot+1:]
}

func derivePerRequestBetas(model ResolvedModel, perCtx map[string]string) []string {
	if len(perCtx) == 0 {
		return nil
	}
	var out []string
	for suffix, beta := range perCtx {
		if beta == "" {
			continue
		}
		if strings.Contains(model.ClaudeModel, suffix) {
			out = append(out, beta)
		}
	}
	return out
}

// stripContextSuffix removes a bracketed wire suffix from the model id.
func stripContextSuffix(model string) string {
	if i := strings.Index(model, "["); i > 0 {
		return model[:i]
	}
	return model
}

// anthropicMaxTokens picks a max_tokens value: caller-supplied when
// positive, otherwise the package default.
func anthropicMaxTokens(req *int) int {
	if req != nil && *req > 0 {
		return *req
	}
	return anthropic.MaxOutputTokens
}
