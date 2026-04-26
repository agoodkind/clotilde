package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic/fallback"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

// handleFallback fulfils a chat completion via the local `claude`
// CLI in `-p --output-format stream-json` mode. It is the third
// dispatch leg, gated by [adapter.fallback].
//
// When escalate is true (the on_oauth_failure / both triggers fired
// after an OAuth error), the function returns a non-nil error
// without writing the response on transport-level failures so the
// dispatcher can decide which error surfaces to the client per
// FailureEscalation. When escalate is false (explicit dispatch),
// errors are written to w directly.
func (s *Server) handleFallback(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, reqID string, escalate bool) error {
	if s.fb == nil {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("fallback_unconfigured: adapter built without fallback client"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"fallback_unconfigured",
			"adapter built without fallback client; set adapter.fallback.enabled=true and restart",
		); err != nil {
			return err
		}
		return nil
	}
	if model.CLIAlias == "" {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("fallback_no_cli_alias: family %q has no CLI alias bound", model.FamilySlug),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadRequest,
			"fallback_no_cli_alias",
			"alias resolves to a family with no [adapter.fallback.cli_aliases] entry; cannot dispatch via claude -p",
		); err != nil {
			return err
		}
		return nil
	}
	if req.Stream && !s.cfg.Fallback.StreamPassthrough {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("fallback_stream_disabled: stream_passthrough=false"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadRequest,
			"fallback_stream_disabled",
			"this adapter is configured with stream_passthrough=false; pass stream=false",
		); err != nil {
			return err
		}
		return nil
	}

	if err := s.acquireFallback(r.Context()); err != nil {
		if err2 := adapterruntime.EscalateOrWrite(
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
	defer s.releaseFallback()

	if s.cfg.Fallback.DropUnsupported {
		if req.ReasoningEffort != "" {
			s.log.LogAttrs(r.Context(), slog.LevelDebug, "adapter.fallback.dropped_field",
				slog.String("request_id", reqID),
				slog.String("field", "reasoning_effort"),
				slog.String("value", req.ReasoningEffort),
				slog.String("reason", "claude -p has no effort flag; planned via settings.json injection"),
			)
		}
		if model.Thinking != "" && model.Thinking != ThinkingDefault {
			s.log.LogAttrs(r.Context(), slog.LevelDebug, "adapter.fallback.dropped_field",
				slog.String("request_id", reqID),
				slog.String("field", "thinking"),
				slog.String("value", model.Thinking),
				slog.String("reason", "claude -p has no thinking flag; planned via settings.json injection"),
			)
		}
	}

	fbReq := fallback.BuildRequest(fallback.RequestBuildInput{
		Model:      model.CLIAlias,
		ModelAlias: model.Alias,
		Messages:   req.Messages,
		Tools:      req.Tools,
		Functions:  req.Functions,
		ToolChoice: req.ToolChoice,
		RequestID:  reqID,
	})
	system := fbReq.System
	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	if instr := jsonSpec.SystemPrompt(false); instr != "" {
		if system == "" {
			system = instr
		} else {
			system = system + "\n\n" + instr
		}
		fbReq.System = system
	}

	if s.cfg.Fallback.TranscriptSynthesisEnabled {
		workspaceDir := s.cfg.Fallback.ResolveTranscriptWorkspaceDir(model.Alias)
		resume := fallback.PrepareTranscriptResume(&fbReq, fallback.TranscriptResumeConfig{
			WorkspaceDir: workspaceDir,
		})
		if resume.Attempted {
			if resume.Err != nil {
				s.log.LogAttrs(r.Context(), slog.LevelWarn, "fallback.transcript.write_failed",
					slog.String("request_id", reqID),
					slog.String("session_id", resume.SessionID),
					slog.String("workspace_dir", resume.WorkspaceDir),
					slog.Any("err", resume.Err),
				)
			} else if resume.Resumed {
				s.log.LogAttrs(r.Context(), slog.LevelDebug, "fallback.transcript.resumed",
					slog.String("request_id", reqID),
					slog.String("session_id", resume.SessionID),
					slog.String("path", resume.Path),
					slog.Int("prior_turns", resume.PriorTurns),
				)
			}
		}
	}

	started := time.Now()
	if req.Stream {
		// Always emit the final usage chunk; see oauth_handler.go for rationale.
		_ = req.StreamOptions
		return s.streamFallback(w, r, fbReq, model, reqID, started, escalate, true)
	}
	return s.collectFallback(w, r.Context(), fbReq, model, reqID, started, jsonSpec, escalate)
}

func (s *Server) collectFallback(w http.ResponseWriter, ctx context.Context, req fallback.Request, model ResolvedModel, reqID string, started time.Time, jsonSpec JSONResponseSpec, escalate bool) error {
	s.emitRequestStarted(ctx, model, "fallback", reqID, req.Model, false)
	var coerce fallback.TextCoercer
	if jsonSpec.Mode != "" {
		coerce = func(text string) (string, bool) {
			coerced := CoerceJSON(text)
			return coerced, LooksLikeJSON(coerced)
		}
	}
	run, err := s.fb.CollectOpenAI(ctx, req, fallback.CollectOpenAIInput{
		RequestID:         reqID,
		ModelAlias:        model.Alias,
		SystemFingerprint: systemFingerprint,
		CoerceText:        coerce,
	})
	if err != nil {
		adapterruntime.LogFailed(s.log, ctx, adapterruntime.FailedAttrs{
			Backend:    "fallback",
			Provider:   providerName(model, "fallback"),
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Err:        err,
			DurationMs: time.Since(started).Milliseconds(),
		})
		adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
			Provider:   providerName(model, "fallback"),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Stream:     false,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
		if err := adapterruntime.EscalateOrWrite(
			err,
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadGateway,
			"fallback_error",
			err.Error(),
		); err != nil {
			return err
		}
		return nil
	}
	result := run.Raw
	final := run.Final
	writeJSON(w, http.StatusOK, final.Response)
	s.logCacheUsage(ctx, "fallback", reqID, model.Alias,
		result.Usage.PromptTokens, result.Usage.CacheCreationInputTokens, result.Usage.CacheReadInputTokens)
	adapterruntime.LogCompleted(s.log, ctx, adapterruntime.CompletedAttrs{
		Backend:             "fallback",
		Provider:            providerName(model, "fallback"),
		Path:                fallback.PathLabel(req),
		SessionID:           req.SessionID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        final.FinishReason,
		TokensIn:            final.Usage.PromptTokens,
		TokensOut:           final.Usage.CompletionTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheTTL:            s.cfg.ClientIdentity.PromptCacheTTL,
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              false,
	})
	breakdown := adapterruntime.EstimateCost(adapterruntime.CostInputs{
		ModelID:             req.Model,
		TTL:                 s.cfg.ClientIdentity.PromptCacheTTL,
		InputTokens:         final.Usage.PromptTokens,
		OutputTokens:        final.Usage.CompletionTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
	})
	adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
		Stage:               adapterruntime.RequestStageCompleted,
		Provider:            providerName(model, "fallback"),
		Backend:             model.Backend,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		Stream:              false,
		FinishReason:        final.FinishReason,
		TokensIn:            final.Usage.PromptTokens,
		TokensOut:           final.Usage.CompletionTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CostMicrocents:      breakdown.TotalMicrocents,
		DurationMs:          time.Since(started).Milliseconds(),
	})
	return nil
}

// streamFallback streams stream-json from the CLI. When tools are
// active (non-none tool_choice), stdout text is buffered inside
// fallback.Stream so JSON envelopes are not split across SSE
// chunks; after the subprocess exits, this handler emits synthetic
// deltas (role, tool_calls, finish_reason) that match OpenAI
// clients. Plain tool_choice "none" keeps live text deltas.
func (s *Server) streamFallback(w http.ResponseWriter, r *http.Request, req fallback.Request, model ResolvedModel, reqID string, started time.Time, escalate bool, includeUsage bool) error {
	s.emitRequestStarted(r.Context(), model, "fallback", reqID, req.Model, true)
	sw, err := newSSEWriter(w)
	if err != nil {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("no_flusher: streaming not supported by transport"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"no_flusher",
			"streaming not supported by this transport",
		); err != nil {
			return err
		}
		return nil
	}

	sw.WriteSSEHeaders()
	s.emitRequestStreamOpened(r.Context(), model, "fallback", reqID, req.Model, true)

	created := time.Now().Unix()

	emit := func(chunk StreamChunk) error {
		return sw.EmitStreamChunk(systemFingerprint, chunk)
	}

	run, streamErr := s.fb.StreamOpenAI(r.Context(), req, fallback.StreamOpenAIInput{
		RequestID:  reqID,
		ModelAlias: model.Alias,
		Created:    created,
	}, emit)
	if streamErr != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.chat.stream_error",
			slog.String("backend", "fallback"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("cli_model", req.Model),
			slog.Any("err", streamErr),
		)
		if escalate && !sw.HasCommittedHeaders() {
			return streamErr
		}
		adapterruntime.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
			Provider:   providerName(model, "fallback"),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Stream:     true,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        streamErr.Error(),
		})
	}

	sr := run.Raw
	plan := run.Plan
	_ = adapterruntime.EmitFinishChunk(emit, reqID, model.Alias, created, plan.FinishReason)

	if includeUsage {
		_ = adapterruntime.EmitUsageChunk(emit, reqID, model.Alias, created, plan.Usage)
	}
	_ = sw.WriteStreamDone()
	s.logCacheUsage(r.Context(), "fallback", reqID, model.Alias,
		sr.Usage.PromptTokens, sr.Usage.CacheCreationInputTokens, sr.Usage.CacheReadInputTokens)
	adapterruntime.LogCompleted(s.log, r.Context(), adapterruntime.CompletedAttrs{
		Backend:             "fallback",
		Provider:            providerName(model, "fallback"),
		Path:                fallback.PathLabel(req),
		SessionID:           req.SessionID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        plan.FinishReason,
		TokensIn:            plan.Usage.PromptTokens,
		TokensOut:           plan.Usage.CompletionTokens,
		CacheReadTokens:     sr.Usage.CacheReadInputTokens,
		CacheCreationTokens: sr.Usage.CacheCreationInputTokens,
		CacheTTL:            s.cfg.ClientIdentity.PromptCacheTTL,
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              true,
	})
	if streamErr == nil {
		breakdown := adapterruntime.EstimateCost(adapterruntime.CostInputs{
			ModelID:             req.Model,
			TTL:                 s.cfg.ClientIdentity.PromptCacheTTL,
			InputTokens:         plan.Usage.PromptTokens,
			OutputTokens:        plan.Usage.CompletionTokens,
			CacheCreationTokens: sr.Usage.CacheCreationInputTokens,
			CacheReadTokens:     sr.Usage.CacheReadInputTokens,
		})
		adapterruntime.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, adapterruntime.RequestEvent{
			Stage:               adapterruntime.RequestStageCompleted,
			Provider:            providerName(model, "fallback"),
			Backend:             model.Backend,
			RequestID:           reqID,
			Alias:               model.Alias,
			ModelID:             req.Model,
			Stream:              true,
			FinishReason:        plan.FinishReason,
			TokensIn:            plan.Usage.PromptTokens,
			TokensOut:           plan.Usage.CompletionTokens,
			CacheReadTokens:     sr.Usage.CacheReadInputTokens,
			CacheCreationTokens: sr.Usage.CacheCreationInputTokens,
			CostMicrocents:      breakdown.TotalMicrocents,
			DurationMs:          time.Since(started).Milliseconds(),
		})
	}
	return nil
}

// acquireFallback waits on the fallback's own concurrency semaphore.
func (s *Server) acquireFallback(ctx context.Context) error {
	select {
	case s.fbSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for fallback concurrency slot")
	}
}

func (s *Server) releaseFallback() {
	select {
	case <-s.fbSem:
	default:
	}
}
