package adapter

import (
	"log/slog"
	"net/http"
	"strings"

	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
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
		writeError(w, http.StatusBadRequest, "unsupported_backend",
			"model resolved to unsupported backend "+model.Backend+"; configure anthropic, codex, shunt, or fallback explicitly")
		return
	}
}
