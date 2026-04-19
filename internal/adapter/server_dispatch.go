package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	entries := s.registry.List()
	resp := ModelsResponse{Object: "list"}
	for _, m := range entries {
		resp.Data = append(resp.Data, ModelEntry{
			ID:          m.Alias,
			Object:      "model",
			OwnedBy:     "clyde",
			Context:     m.Context,
			Efforts:     m.Efforts,
			Backend:     m.Backend,
			ClaudeModel: m.ClaudeModel,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	reqID := newRequestID()
	w.Header().Set("x-clyde-request-id", reqID)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to read body")
		return
	}
	bodyBytes := len(body)
	logBody := body
	bodyTruncated := false
	if bodyBytes > rawChatBodyLimit {
		logBody = body[:rawChatBodyLimit]
		bodyTruncated = true
	}
	rawAttrs := rawChatLogEvent{
		RequestID:     reqID,
		Method:        r.Method,
		Path:          r.URL.Path,
		RemoteAddr:    r.RemoteAddr,
		Headers:       redactedHeaders(r.Header),
		BodyBytes:     bodyBytes,
		Body:          string(logBody),
		BodyTruncated: bodyTruncated,
	}
	s.log.LogAttrs(r.Context(), slog.LevelDebug, "adapter.chat.raw", rawAttrs.asAttrs()...)

	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "messages is required")
		return
	}

	model, effort, err := s.registry.Resolve(req.Model, req.ReasoningEffort)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown_model", err.Error())
		return
	}

	toolNames := make([]string, 0, len(req.Tools)+len(req.Functions))
	for _, t := range req.Tools {
		toolNames = append(toolNames, t.Function.Name)
	}
	for _, f := range req.Functions {
		toolNames = append(toolNames, f.Name)
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.received",
		slog.String("request_id", reqID),
		slog.String("alias", req.Model),
		slog.String("backend", string(model.Backend)),
		slog.Int("message_count", len(req.Messages)),
		slog.Int("tool_count", len(req.Tools)+len(req.Functions)),
		slog.Any("tool_names", toolNames),
		slog.Bool("stream", req.Stream),
	)

	if perr := s.preflightChat(r.Context(), &req, model, reqID); perr != nil {
		writeJSON(w, perr.code, ErrorResponse{Error: perr.body})
		return
	}

	if model.Backend == BackendShunt {
		s.forwardShunt(w, r, model, body)
		return
	}

	if model.Backend == BackendFallback {
		// Explicit-mode dispatch: alias is bound to the fallback
		// backend directly, no OAuth attempt is made.
		_ = s.handleFallback(w, r, req, model, reqID, false)
		return
	}

	if model.Backend == BackendAnthropic {
		s.dispatchAnthropicWithFallback(w, r, req, model, effort, reqID, body)
		return
	}

	if err := s.acquire(r.Context()); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
		return
	}
	defer s.release()

	system, prompt := BuildPrompt(req.Messages)
	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	if instr := jsonSpec.SystemPrompt(false); instr != "" {
		if system == "" {
			system = instr
		} else {
			system = system + "\n\n" + instr
		}
	}
	runner := NewRunner(s.deps, model, effort, system, prompt, reqID)
	started := time.Now()
	stdout, cancel, err := runner.Spawn(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "spawn_failed", err.Error())
		return
	}
	defer cancel()

	if req.Stream {
		// Streaming JSON enforcement is impractical because chunks
		// arrive token-by-token and cannot be re-issued mid-stream.
		// The system prompt already nudges claude toward raw JSON;
		// pure structured-output clients (humanify, etc.) almost
		// always use the non-streaming path.
		s.streamChat(w, r, req, model, stdout, reqID, started)
		return
	}
	s.collectChat(w, r.Context(), req, model, stdout, reqID, started, jsonSpec)
}

func (s *Server) collectChat(w http.ResponseWriter, ctx context.Context, req ChatRequest, model ResolvedModel, stdout io.ReadCloser, reqID string, started time.Time, jsonSpec JSONResponseSpec) {
	text, usage, err := CollectStream(stdout)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	finalText, usage, jsonRetried := s.legacyCollectApplyStructuredOutput(ctx, req, model, text, usage, jsonSpec, reqID)
	resp := ChatResponse{
		ID:                reqID,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             model.Alias,
		SystemFingerprint: systemFingerprint,
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:    "assistant",
				Content: json.RawMessage(strconv.Quote(finalText)),
			},
			FinishReason: "stop",
		}},
		Usage: &usage,
	}
	writeJSON(w, http.StatusOK, resp)
	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.chat.completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", false),
		slog.String("json_mode", jsonSpec.Mode),
		slog.Bool("json_retried", jsonRetried),
	)
}

func (s *Server) handleLegacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var legacy struct {
		Model           string `json:"model"`
		Prompt          string `json:"prompt"`
		Stream          bool   `json:"stream,omitempty"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&legacy); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	synthetic := ChatRequest{
		Model:           legacy.Model,
		Stream:          legacy.Stream,
		ReasoningEffort: legacy.ReasoningEffort,
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(strconv.Quote(legacy.Prompt)),
		}},
	}
	body, _ := json.Marshal(synthetic)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleChat(w, r)
}

// dispatchAnthropicWithFallback runs the direct-Anthropic backend
// (Bearer auth via the OAuth keychain token) and, when the
// configured trigger covers on_oauth_failure, escalates to either
// the configured forward_to_shunt or the `claude -p` fallback.
// FailureEscalation picks whether the Anthropic or the fallback
// error surfaces when both fail.
//
// When fallback is disabled or the trigger does not cover Anthropic
// failures, the function delegates to s.handleOAuth directly with
// escalate=false (preserving the pre-fallback behavior).
func (s *Server) dispatchAnthropicWithFallback(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, body []byte) {
	fb := s.cfg.Fallback
	escalate := fb.Enabled &&
		(fb.Trigger == FallbackTriggerOnOAuthFailure || fb.Trigger == FallbackTriggerBoth)

	if !escalate {
		_ = s.handleOAuth(w, r, req, model, effort, reqID, false)
		return
	}

	anthErr := s.handleOAuth(w, r, req, model, effort, reqID, true)
	if anthErr == nil {
		return
	}

	s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.fallback.escalating",
		slog.String("request_id", reqID),
		slog.String("alias", model.Alias),
		slog.String("anthropic_err", anthErr.Error()),
		slog.Bool("forward_to_shunt", fb.ForwardToShunt.Enabled),
	)

	if fb.ForwardToShunt.Enabled {
		shunt, ok := s.registry.Shunt(fb.ForwardToShunt.Shunt)
		if !ok || shunt.BaseURL == "" {
			s.log.LogAttrs(r.Context(), slog.LevelError, "adapter.fallback.shunt_unconfigured",
				slog.String("request_id", reqID),
				slog.String("shunt", fb.ForwardToShunt.Shunt),
			)
			s.surfaceFallbackFailure(w, anthErr, fmt.Errorf(
				"forward_to_shunt %q not configured (base_url empty)", fb.ForwardToShunt.Shunt))
			return
		}
		// Reuse the existing shunt path; ResolvedModel.Shunt is
		// the lookup key for forwardShunt.
		shuntModel := model
		shuntModel.Backend = BackendShunt
		shuntModel.Shunt = fb.ForwardToShunt.Shunt
		s.forwardShunt(w, r, shuntModel, body)
		return
	}

	if s.fb == nil {
		s.surfaceFallbackFailure(w, anthErr, fmt.Errorf("fallback client not constructed"))
		return
	}
	if model.CLIAlias == "" {
		s.surfaceFallbackFailure(w, anthErr, fmt.Errorf(
			"family %q has no [adapter.fallback.cli_aliases] entry; cannot escalate", model.FamilySlug))
		return
	}

	fbErr := s.handleFallback(w, r, req, model, reqID, true)
	if fbErr == nil {
		return
	}
	s.surfaceFallbackFailure(w, anthErr, fbErr)
}

// surfaceFallbackFailure writes the error chosen by
// FailureEscalation. Called only after both attempts have failed
// and nothing has been written to the wire yet (the escalate=true
// path returns before any header/byte commits).
func (s *Server) surfaceFallbackFailure(w http.ResponseWriter, anthErr, fbErr error) {
	switch s.cfg.Fallback.FailureEscalation {
	case FallbackEscalationOAuthError:
		writeError(w, http.StatusBadGateway, "upstream_error", anthErr.Error())
	default: // FallbackEscalationFallbackError
		writeError(w, http.StatusBadGateway, "fallback_error", fbErr.Error())
	}
}
