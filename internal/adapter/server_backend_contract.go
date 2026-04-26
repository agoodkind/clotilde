package adapter

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
	"goodkind.io/gklog"
)

// applyBackendOverride keeps backend selection in the root adapter so request
// decode, model resolution, and final backend choice stay readable in one
// place before control passes to a backend package.
func (s *Server) applyBackendOverride(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, reqID string) (ResolvedModel, bool) {
	override := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Clyde-Backend")))
	if override == "" {
		return model, true
	}

	original := string(model.Backend)
	switch override {
	case BackendAnthropic, BackendFallback, BackendShunt, BackendCodex:
		model.Backend = override
	default:
		writeError(w, http.StatusBadRequest, "invalid_backend_override",
			"X-Clyde-Backend must be one of: anthropic, fallback, shunt, codex")
		return model, false
	}

	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.backend.overridden",
		slog.String("request_id", reqID),
		slog.String("alias", req.Model),
		slog.String("from", original),
		slog.String("to", override),
	)
	return model, true
}

// dispatchResolvedChat is the root-owned backend contract boundary. By the time
// execution reaches this method, the request has already been decoded,
// normalized, logged, and resolved to a backend.
func (s *Server) dispatchResolvedChat(
	w http.ResponseWriter,
	r *http.Request,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	body []byte,
) {
	switch model.Backend {
	case BackendShunt:
		s.forwardShunt(w, r, model, body)
		return
	case BackendFallback:
		// Explicit-mode dispatch: alias is bound to the fallback backend
		// directly, so no OAuth attempt is made first.
		_ = s.handleFallback(w, r, req, model, reqID, false)
		return
	case BackendAnthropic:
		anthropicbackend.Dispatch(s, anthropicbackend.FallbackConfig{
			Enabled:            s.cfg.Fallback.Enabled,
			Trigger:            s.cfg.Fallback.Trigger,
			ForwardToShunt:     s.cfg.Fallback.ForwardToShunt.Enabled,
			ForwardToShuntName: s.cfg.Fallback.ForwardToShunt.Shunt,
			FailureEscalation:  s.cfg.Fallback.FailureEscalation,
		}, w, r, req, model, effort, reqID, body)
		return
	case BackendCodex:
		if err := adaptercodex.Dispatch(s, w, r, req, model, effort, reqID); err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	default:
		s.dispatchLegacyChat(w, r, req, model, effort, reqID)
		return
	}
}

func (s *Server) dispatchLegacyChat(
	w http.ResponseWriter,
	r *http.Request,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
) {
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
	s.emitRequestStarted(r.Context(), model, "", reqID, model.ClaudeModel, req.Stream)
	spawnCtx := gklog.WithLogger(r.Context(), s.log.With("request_id", reqID))
	stdout, cancel, err := runner.Spawn(spawnCtx)
	if err != nil {
		adapterruntime.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
			Provider:   providerName(model, ""),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.ClaudeModel,
			Stream:     req.Stream,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
		writeError(w, http.StatusInternalServerError, "spawn_failed", err.Error())
		return
	}
	defer cancel()

	if req.Stream {
		// Streaming JSON enforcement is impractical because chunks arrive
		// token-by-token and cannot be re-issued mid-stream.
		s.streamChat(w, r, req, model, stdout, reqID, started)
		return
	}

	s.collectChat(w, r.Context(), req, model, stdout, reqID, started, jsonSpec)
}
