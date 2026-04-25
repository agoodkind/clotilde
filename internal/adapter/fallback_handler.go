package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/chatemit"
	"goodkind.io/clyde/internal/adapter/fallback"
	"goodkind.io/clyde/internal/adapter/finishreason"
)

// writeFallbackTranscript materializes a synthesized Claude Code
// transcript on disk so the subsequent `claude -p --resume` call can
// read conversation history from the JSONL rather than reprocessing
// a flattened positional prompt. Writes under
// ~/.claude/projects/<sanitize(workspaceDir)>/<session-id>.jsonl.
//
// Only prior turns are serialized (msgs[:-1] effectively), because the
// last user message rides in as the positional prompt on spawn. When
// the message set is shorter than one turn, writing is skipped and
// the caller falls back to the legacy --session-id path.
func (s *Server) writeFallbackTranscript(ctx context.Context, workspaceDir, sessionID string, msgs []fallback.Message) error {
	// Ensure the cwd exists so claude -p does not fail at spawn with
	// a missing directory. The mkdir is idempotent.
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return fmt.Errorf("mkdir workspace: %w", err)
	}
	// Find the final user message so we can exclude it from the
	// transcript (it becomes the positional prompt).
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser <= 0 {
		// No prior history to materialize; the handler stays on the
		// legacy --session-id path and does a fresh conversation.
		return fmt.Errorf("no prior turns to synthesize")
	}
	priorMsgs := msgs[:lastUser]
	lines, err := fallback.SynthesizeTranscript(priorMsgs, sessionID, workspaceDir, time.Now())
	if err != nil {
		return fmt.Errorf("synthesize: %w", err)
	}
	claudeHome := claudeConfigHome()
	path := fallback.TranscriptPath(claudeHome, workspaceDir, sessionID)
	if err := fallback.WriteTranscript(path, lines); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	s.log.LogAttrs(ctx, slog.LevelDebug, "fallback.transcript.written",
		slog.String("session_id", sessionID),
		slog.String("path", path),
		slog.Int("lines", len(lines)),
		slog.Int("prior_turns", lastUser),
	)
	return nil
}

// claudeConfigHome resolves $CLAUDE_CONFIG_HOME, falling back to
// ~/.claude. Matches the resolution in
// src/utils/sessionStorage.ts:202-204.
func claudeConfigHome() string {
	if v := os.Getenv("CLAUDE_CONFIG_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}

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
		if err := chatemit.EscalateOrWrite(
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
		if err := chatemit.EscalateOrWrite(
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
		if err := chatemit.EscalateOrWrite(
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

	system, msgs := buildFallbackMessages(req.Messages)
	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	if instr := jsonSpec.SystemPrompt(false); instr != "" {
		if system == "" {
			system = instr
		} else {
			system = system + "\n\n" + instr
		}
	}

	sessionID := deriveFallbackSessionID(msgs, model.Alias)
	fbReq := fallback.Request{
		Model:      model.CLIAlias,
		System:     system,
		Messages:   msgs,
		Tools:      buildFallbackTools(req),
		ToolChoice: parseFallbackToolChoice(req.ToolChoice),
		RequestID:  reqID,
		SessionID:  sessionID,
	}
	// Phase 3: synthesize a Claude Code transcript on disk so the CLI
	// --resumes it instead of re-flattening history every turn. Opt-in
	// via [adapter.fallback].transcript_synthesis_enabled. When on,
	// the write lands under a dedicated workspace dir (never mingles
	// with real workspaces or clyde sessions) and we pass --resume so
	// Claude's own prompt caching pipeline fires on every turn.
	if s.cfg.Fallback.TranscriptSynthesisEnabled && len(msgs) > 0 && sessionID != "" {
		workspaceDir := s.cfg.Fallback.ResolveTranscriptWorkspaceDir(model.Alias)
		if workspaceDir != "" {
			if err := s.writeFallbackTranscript(r.Context(), workspaceDir, sessionID, msgs); err != nil {
				s.log.LogAttrs(r.Context(), slog.LevelWarn, "fallback.transcript.write_failed",
					slog.String("request_id", reqID),
					slog.String("session_id", sessionID),
					slog.String("workspace_dir", workspaceDir),
					slog.Any("err", err),
				)
			} else {
				fbReq.Resume = true
				fbReq.WorkspaceDir = workspaceDir
				s.log.LogAttrs(r.Context(), slog.LevelDebug, "fallback.transcript.resumed",
					slog.String("request_id", reqID),
					slog.String("session_id", sessionID),
					slog.Int("prior_turns", len(msgs)-1),
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
	result, err := s.fb.Collect(ctx, req)
	if err != nil {
		chatemit.LogFailed(s.log, ctx, chatemit.FailedAttrs{
			Backend:    "fallback",
			Provider:   providerName(model, "fallback"),
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Err:        err,
			DurationMs: time.Since(started).Milliseconds(),
		})
		chatemit.LogTerminal(s.log, ctx, s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   providerName(model, "fallback"),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Stream:     false,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
		if err := chatemit.EscalateOrWrite(
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
	finalText := result.Text
	if jsonSpec.Mode != "" && result.Refusal == "" {
		coerced := CoerceJSON(result.Text)
		if LooksLikeJSON(coerced) {
			finalText = coerced
		}
	}
	usage := Usage{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.TotalTokens,
	}
	if result.Usage.CacheReadInputTokens > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: result.Usage.CacheReadInputTokens}
	}
	msg := ChatMessage{Role: "assistant"}
	if result.ReasoningContent != "" {
		msg.Reasoning = result.ReasoningContent
		msg.ReasoningContent = result.ReasoningContent
	}
	if result.Refusal != "" {
		msg.Refusal = result.Refusal
		msg.Content = json.RawMessage("null")
	} else if len(result.ToolCalls) > 0 {
		msg.ToolCalls = make([]ToolCall, len(result.ToolCalls))
		for i, tc := range result.ToolCalls {
			msg.ToolCalls[i] = ToolCall{
				Index: i,
				ID:    fallback.EnsureToolCallID(tc.ID, reqID, i),
				Type:  "function",
				Function: ToolCallFunction{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			}
		}
		if finalText == "" {
			msg.Content = json.RawMessage("null")
		} else {
			msg.Content = json.RawMessage(strconv.Quote(finalText))
		}
	} else {
		msg.Content = json.RawMessage(strconv.Quote(finalText))
	}
	fr := finishreason.FromAnthropicNonStream(result.Stop)
	resp := ChatResponse{
		ID:                reqID,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             model.Alias,
		SystemFingerprint: systemFingerprint,
		Choices: []ChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: fr,
		}},
		Usage: &usage,
	}
	writeJSON(w, http.StatusOK, resp)
	s.logCacheUsage(ctx, "fallback", reqID, model.Alias,
		result.Usage.PromptTokens, result.Usage.CacheCreationInputTokens, result.Usage.CacheReadInputTokens)
	chatemit.LogCompleted(s.log, ctx, chatemit.CompletedAttrs{
		Backend:             "fallback",
		Provider:            providerName(model, "fallback"),
		Path:                fallbackPathLabel(req),
		SessionID:           req.SessionID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        fr,
		TokensIn:            usage.PromptTokens,
		TokensOut:           usage.CompletionTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheTTL:            s.cfg.ClientIdentity.PromptCacheTTL,
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              false,
	})
	breakdown := chatemit.EstimateCost(chatemit.CostInputs{
		ModelID:             req.Model,
		TTL:                 s.cfg.ClientIdentity.PromptCacheTTL,
		InputTokens:         usage.PromptTokens,
		OutputTokens:        usage.CompletionTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
	})
	chatemit.LogTerminal(s.log, ctx, s.deps.RequestEvents, chatemit.RequestEvent{
		Stage:               chatemit.RequestStageCompleted,
		Provider:            providerName(model, "fallback"),
		Backend:             model.Backend,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		Stream:              false,
		FinishReason:        fr,
		TokensIn:            usage.PromptTokens,
		TokensOut:           usage.CompletionTokens,
		CacheReadTokens:     result.Usage.CacheReadInputTokens,
		CacheCreationTokens: result.Usage.CacheCreationInputTokens,
		CostMicrocents:      breakdown.TotalMicrocents,
		DurationMs:          time.Since(started).Milliseconds(),
	})
	return nil
}

// fallbackPathLabel picks the dispatch tag for log events based on
// whether the request rides the synthesized-transcript resume
// pathway. Used by the cost aggregator to compare cache efficiency
// across legs.
func fallbackPathLabel(req fallback.Request) string {
	if req.Resume {
		return "fallback_resume"
	}
	return "fallback_flat"
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
		if err := chatemit.EscalateOrWrite(
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

	sw.writeSSEHeaders()
	s.emitRequestStreamOpened(r.Context(), model, "fallback", reqID, req.Model, true)

	created := time.Now().Unix()
	firstDelta := true

	emit := func(chunk StreamChunk) error {
		return sw.emitStreamChunk(systemFingerprint, chunk)
	}

	bufferedTools := len(req.Tools) > 0 && strings.ToLower(strings.TrimSpace(req.ToolChoice)) != "none"
	var sr fallback.StreamResult
	var streamErr error
	if bufferedTools {
		sr, streamErr = s.fb.Stream(r.Context(), req, func(fallback.StreamEvent) error { return nil })
	} else {
		sr, streamErr = s.fb.Stream(r.Context(), req, func(ev fallback.StreamEvent) error {
			delta := StreamDelta{}
			switch ev.Kind {
			case "text":
				delta.Content = ev.Text
			case "reasoning":
				delta.Reasoning = ev.Text
				delta.ReasoningContent = ev.Text
			default:
				return nil
			}
			if firstDelta {
				delta.Role = "assistant"
				firstDelta = false
			}
			return emit(StreamChunk{
				ID:      reqID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model.Alias,
				Choices: []StreamChoice{{
					Index: 0,
					Delta: delta,
				}},
			})
		})
	}
	if streamErr != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.chat.stream_error",
			slog.String("backend", "fallback"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("cli_model", req.Model),
			slog.Any("err", streamErr),
		)
		if escalate && !sw.hasCommittedHeaders() {
			return streamErr
		}
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
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

	finalFinish := finishreason.FromAnthropicNonStream(sr.Stop)

	if bufferedTools {
		if len(sr.ToolCalls) > 0 {
			if rc := strings.TrimSpace(sr.ReasoningContent); rc != "" {
				_ = emit(StreamChunk{
					ID:      reqID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model.Alias,
					Choices: []StreamChoice{{
						Index: 0,
						Delta: StreamDelta{
							Role:             "assistant",
							Reasoning:        rc,
							ReasoningContent: rc,
						},
					}},
				})
			} else {
				_ = emit(StreamChunk{
					ID:      reqID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model.Alias,
					Choices: []StreamChoice{{
						Index: 0,
						Delta: StreamDelta{Role: "assistant"},
					}},
				})
			}
			for i, tc := range sr.ToolCalls {
				tid := fallback.EnsureToolCallID(tc.ID, reqID, i)
				_ = emit(StreamChunk{
					ID:      reqID,
					Object:  "chat.completion.chunk",
					Created: created,
					Model:   model.Alias,
					Choices: []StreamChoice{{
						Index: 0,
						Delta: StreamDelta{
							ToolCalls: []ToolCall{{
								Index: i,
								ID:    tid,
								Type:  "function",
								Function: ToolCallFunction{
									Name:      tc.Name,
									Arguments: tc.Arguments,
								},
							}},
						},
					}},
				})
			}
			finalFinish = "tool_calls"
		} else {
			d := StreamDelta{Role: "assistant", Content: sr.Text}
			if rc := strings.TrimSpace(sr.ReasoningContent); rc != "" {
				d.Reasoning = rc
				d.ReasoningContent = rc
			}
			_ = emit(StreamChunk{
				ID:      reqID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model.Alias,
				Choices: []StreamChoice{{
					Index: 0,
					Delta: d,
				}},
			})
			finalFinish = finishreason.FromAnthropicNonStream(sr.Stop)
		}
	} else if strings.EqualFold(sr.Stop, "refusal") {
		finalFinish = finishreason.FromAnthropicNonStream(sr.Stop)
	} else {
		finalFinish = finishreason.FromAnthropicNonStream(sr.Stop)
	}
	_ = chatemit.EmitFinishChunk(emit, reqID, model.Alias, created, finalFinish)

	finalUsage := Usage{
		PromptTokens:     sr.Usage.PromptTokens,
		CompletionTokens: sr.Usage.CompletionTokens,
		TotalTokens:      sr.Usage.TotalTokens,
	}
	if sr.Usage.CacheReadInputTokens > 0 {
		finalUsage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: sr.Usage.CacheReadInputTokens}
	}
	if includeUsage {
		_ = chatemit.EmitUsageChunk(emit, reqID, model.Alias, created, finalUsage)
	}
	_ = sw.writeStreamDone()
	s.logCacheUsage(r.Context(), "fallback", reqID, model.Alias,
		sr.Usage.PromptTokens, sr.Usage.CacheCreationInputTokens, sr.Usage.CacheReadInputTokens)
	chatemit.LogCompleted(s.log, r.Context(), chatemit.CompletedAttrs{
		Backend:             "fallback",
		Provider:            providerName(model, "fallback"),
		Path:                fallbackPathLabel(req),
		SessionID:           req.SessionID,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             req.Model,
		FinishReason:        finalFinish,
		TokensIn:            finalUsage.PromptTokens,
		TokensOut:           finalUsage.CompletionTokens,
		CacheReadTokens:     sr.Usage.CacheReadInputTokens,
		CacheCreationTokens: sr.Usage.CacheCreationInputTokens,
		CacheTTL:            s.cfg.ClientIdentity.PromptCacheTTL,
		DurationMs:          time.Since(started).Milliseconds(),
		Stream:              true,
	})
	if streamErr == nil {
		breakdown := chatemit.EstimateCost(chatemit.CostInputs{
			ModelID:             req.Model,
			TTL:                 s.cfg.ClientIdentity.PromptCacheTTL,
			InputTokens:         finalUsage.PromptTokens,
			OutputTokens:        finalUsage.CompletionTokens,
			CacheCreationTokens: sr.Usage.CacheCreationInputTokens,
			CacheReadTokens:     sr.Usage.CacheReadInputTokens,
		})
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:               chatemit.RequestStageCompleted,
			Provider:            providerName(model, "fallback"),
			Backend:             model.Backend,
			RequestID:           reqID,
			Alias:               model.Alias,
			ModelID:             req.Model,
			Stream:              true,
			FinishReason:        finalFinish,
			TokensIn:            finalUsage.PromptTokens,
			TokensOut:           finalUsage.CompletionTokens,
			CacheReadTokens:     sr.Usage.CacheReadInputTokens,
			CacheCreationTokens: sr.Usage.CacheCreationInputTokens,
			CostMicrocents:      breakdown.TotalMicrocents,
			DurationMs:          time.Since(started).Milliseconds(),
		})
	}
	return nil
}

// buildFallbackTools maps OpenAI tools (preferred) or legacy
// functions into the fallback tool slice. When req.Tools is
// non-empty, legacy functions are ignored so definitions are not
// double-registered.
func buildFallbackTools(req ChatRequest) []fallback.Tool {
	if len(req.Tools) > 0 {
		out := make([]fallback.Tool, 0, len(req.Tools))
		for _, t := range req.Tools {
			if t.Function.Name == "" {
				continue
			}
			out = append(out, fallback.Tool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		return out
	}
	out := make([]fallback.Tool, 0, len(req.Functions))
	for _, f := range req.Functions {
		if f.Name == "" {
			continue
		}
		out = append(out, fallback.Tool{
			Name:        f.Name,
			Description: f.Description,
			Parameters:  f.Parameters,
		})
	}
	return out
}

// parseFallbackToolChoice decodes OpenAI tool_choice as either a
// string token or a typed function selection object.
func parseFallbackToolChoice(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "auto"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	var wrapped struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		if wrapped.Type == "function" && strings.TrimSpace(wrapped.Function.Name) != "" {
			return strings.TrimSpace(wrapped.Function.Name)
		}
	}
	return "auto"
}

// deriveFallbackSessionID returns a UUID stable across turns of the
// same Cursor conversation: Cursor resends the full history on every
// request so the first user message never changes, and hashing it
// with the model alias gives us a deterministic per-conversation
// identifier that we pass to `claude -p` via `--session-id`. Empty
// when the history has no user message — the caller then omits the
// flag and Claude Code allocates its own.
func deriveFallbackSessionID(msgs []fallback.Message, modelAlias string) string {
	return fallback.DeriveSessionID(fallbackFirstUserText(msgs), modelAlias)
}

func fallbackFirstUserText(msgs []fallback.Message) string {
	for _, m := range msgs {
		if strings.EqualFold(strings.TrimSpace(m.Role), "user") {
			return m.Content
		}
	}
	return ""
}

// buildFallbackMessages converts OpenAI-shaped ChatMessages into the
// fallback package's Message slice. Multiple system messages are
// joined; tool/function turns are folded into the user lane the
// same way buildAnthropicMessages does it.
func buildFallbackMessages(in []ChatMessage) (string, []fallback.Message) {
	var sys []string
	var out []fallback.Message
	for _, m := range in {
		text := FlattenContent(m.Content)
		role := stringsToLower(m.Role)
		switch role {
		case "system", "developer":
			if text != "" {
				sys = append(sys, text)
			}
		case "user", "assistant":
			out = appendOrMergeFallback(out, role, text)
		case "tool", "function":
			out = appendOrMergeFallback(out, "user", "tool: "+text)
		default:
			out = appendOrMergeFallback(out, "user", role+": "+text)
		}
	}
	return joinNonEmpty(sys, "\n\n"), out
}

func appendOrMergeFallback(msgs []fallback.Message, role, text string) []fallback.Message {
	if text == "" {
		return msgs
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == role {
		msgs[n-1].Content = msgs[n-1].Content + "\n\n" + text
		return msgs
	}
	return append(msgs, fallback.Message{Role: role, Content: text})
}

// stringsToLower is a tiny shim so the file doesn't pull in
// `strings` purely for one call (it doesn't otherwise need it).
func stringsToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 && out != "" {
			out += sep
		}
		out += p
	}
	return out
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
