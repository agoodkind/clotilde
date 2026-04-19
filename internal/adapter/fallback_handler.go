package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/fallback"
	"goodkind.io/clyde/internal/adapter/finishreason"
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
		if escalate {
			return fmt.Errorf("fallback_unconfigured: adapter built without fallback client")
		}
		writeError(w, http.StatusInternalServerError, "fallback_unconfigured",
			"adapter built without fallback client; set adapter.fallback.enabled=true and restart")
		return nil
	}
	if model.CLIAlias == "" {
		if escalate {
			return fmt.Errorf("fallback_no_cli_alias: family %q has no CLI alias bound", model.FamilySlug)
		}
		writeError(w, http.StatusBadRequest, "fallback_no_cli_alias",
			"alias resolves to a family with no [adapter.fallback.cli_aliases] entry; cannot dispatch via claude -p")
		return nil
	}
	if req.Stream && !s.cfg.Fallback.StreamPassthrough {
		if escalate {
			return fmt.Errorf("fallback_stream_disabled: stream_passthrough=false")
		}
		writeError(w, http.StatusBadRequest, "fallback_stream_disabled",
			"this adapter is configured with stream_passthrough=false; pass stream=false")
		return nil
	}

	if err := s.acquireFallback(r.Context()); err != nil {
		if escalate {
			return fmt.Errorf("rate_limited: %w", err)
		}
		writeError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
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

	fbReq := fallback.Request{
		Model:      model.CLIAlias,
		System:     system,
		Messages:   msgs,
		Tools:      buildFallbackTools(req),
		ToolChoice: parseFallbackToolChoice(req.ToolChoice),
		RequestID:  reqID,
	}

	started := time.Now()
	if req.Stream {
		return s.streamFallback(w, r, fbReq, model, reqID, started, escalate)
	}
	return s.collectFallback(w, r.Context(), fbReq, model, reqID, started, jsonSpec, escalate)
}

func (s *Server) collectFallback(w http.ResponseWriter, ctx context.Context, req fallback.Request, model ResolvedModel, reqID string, started time.Time, jsonSpec JSONResponseSpec, escalate bool) error {
	result, err := s.fb.Collect(ctx, req)
	if err != nil {
		s.log.LogAttrs(ctx, slog.LevelError, "adapter.chat.failed",
			slog.String("backend", "fallback"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("cli_model", req.Model),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
			slog.Any("err", err),
		)
		if escalate {
			return err
		}
		writeError(w, http.StatusBadGateway, "fallback_error", err.Error())
		return nil
	}
	finalText := result.Text
	if jsonSpec.Mode != "" {
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
	msg := ChatMessage{Role: "assistant"}
	if len(result.ToolCalls) > 0 {
		msg.ToolCalls = make([]ToolCall, len(result.ToolCalls))
		for i, tc := range result.ToolCalls {
			msg.ToolCalls[i] = ToolCall{
				Index: i,
				ID:    tc.ID,
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
	resp := ChatResponse{
		ID:                reqID,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             model.Alias,
		SystemFingerprint: systemFingerprint,
		Choices: []ChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishreason.FromAnthropicNonStream(result.Stop),
		}},
		Usage: &usage,
	}
	writeJSON(w, http.StatusOK, resp)
	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.chat.completed",
		slog.String("backend", "fallback"),
		slog.String("request_id", reqID),
		slog.String("alias", model.Alias),
		slog.String("cli_model", req.Model),
		slog.Int("tokens_in", usage.PromptTokens),
		slog.Int("tokens_out", usage.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", false),
	)
	return nil
}

// streamFallback streams stream-json from the CLI. When tools are
// active (non-none tool_choice), stdout text is buffered inside
// fallback.Stream so JSON envelopes are not split across SSE
// chunks; after the subprocess exits, this handler emits synthetic
// deltas (role, tool_calls, finish_reason) that match OpenAI
// clients. Plain tool_choice "none" keeps live text deltas.
func (s *Server) streamFallback(w http.ResponseWriter, r *http.Request, req fallback.Request, model ResolvedModel, reqID string, started time.Time, escalate bool) error {
	sw, err := newSSEWriter(w)
	if err != nil {
		if escalate {
			return fmt.Errorf("no_flusher: streaming not supported by transport")
		}
		writeError(w, http.StatusInternalServerError, "no_flusher", "streaming not supported by this transport")
		return nil
	}

	created := time.Now().Unix()
	firstText := true

	emit := func(chunk StreamChunk) error {
		return sw.emitStreamChunk(systemFingerprint, chunk)
	}

	bufferedTools := len(req.Tools) > 0 && strings.ToLower(strings.TrimSpace(req.ToolChoice)) != "none"
	var sr fallback.StreamResult
	var streamErr error
	if bufferedTools {
		sr, streamErr = s.fb.Stream(r.Context(), req, func(string) error { return nil })
	} else {
		sr, streamErr = s.fb.Stream(r.Context(), req, func(delta string) error {
			chunk := StreamChunk{
				ID:      reqID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model.Alias,
				Choices: []StreamChoice{{
					Index: 0,
					Delta: StreamDelta{Content: delta},
				}},
			}
			if firstText {
				chunk.Choices[0].Delta.Role = "assistant"
				firstText = false
			}
			return emit(chunk)
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
	}
	sw.writeSSEHeaders()

	if bufferedTools {
		if len(sr.ToolCalls) > 0 {
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
			for i, tc := range sr.ToolCalls {
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
								ID:    tc.ID,
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
			toolFinish := "tool_calls"
			_ = emit(StreamChunk{
				ID:      reqID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model.Alias,
				Choices: []StreamChoice{{
					Index:        0,
					Delta:        StreamDelta{},
					FinishReason: &toolFinish,
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
					Delta: StreamDelta{
						Role:    "assistant",
						Content: sr.Text,
					},
				}},
			})
			plainFinish := finishreason.FromAnthropicNonStream(sr.Stop)
			_ = emit(StreamChunk{
				ID:      reqID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   model.Alias,
				Choices: []StreamChoice{{
					Index:        0,
					Delta:        StreamDelta{},
					FinishReason: &plainFinish,
				}},
			})
		}
	} else {
		finishReason := finishreason.FromAnthropicNonStream(sr.Stop)
		_ = emit(StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model.Alias,
			Choices: []StreamChoice{{
				Index:        0,
				Delta:        StreamDelta{},
				FinishReason: &finishReason,
			}},
		})
	}

	finalUsage := Usage{
		PromptTokens:     sr.Usage.PromptTokens,
		CompletionTokens: sr.Usage.CompletionTokens,
		TotalTokens:      sr.Usage.TotalTokens,
	}
	// Always emit usage (matches the OAuth path for parity).
	_ = emit(StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model.Alias,
		Choices: []StreamChoice{},
		Usage:   &finalUsage,
	})
	_ = sw.writeStreamDone()
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed",
		slog.String("backend", "fallback"),
		slog.String("request_id", reqID),
		slog.String("alias", model.Alias),
		slog.String("cli_model", req.Model),
		slog.Int("tokens_in", finalUsage.PromptTokens),
		slog.Int("tokens_out", finalUsage.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", true),
	)
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
