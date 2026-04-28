package adapter

import (
	"log/slog"
	"net/http"
	"strings"

	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
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
	cursorReq adaptercursor.Request,
	resolvedReq adapterresolver.ResolvedRequest,
	resolverErr error,
) {
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.backend.dispatching",
		slog.String("request_id", reqID),
		slog.String("alias", req.Model),
		slog.String("backend", string(model.Backend)),
		slog.String("resolved_model", model.ClaudeModel),
		slog.String("effort", effort),
		slog.Bool("stream", req.Stream),
	)
	switch model.Backend {
	case BackendShunt:
		s.forwardShunt(w, r, model, body)
		return
	case BackendFallback:
		// Explicit-mode dispatch: alias is bound to the fallback backend
		// directly, so no OAuth attempt is made first.
		_ = s.HandleFallback(w, r, req, model, reqID, false)
		return
	case BackendAnthropic:
		// Plan 4 step 1: try the registry path first. The Anthropic
		// Provider is currently a registry shim that returns
		// ErrLegacyDispatchPath; when that fires we fall through to
		// the legacy anthropicbackend.Dispatch chain. The shim is in
		// place so future symmetry-aware code paths can look up the
		// provider by ResolvedRequest.Provider the same way Codex
		// does. The internal rewrite (Provider.Execute owns the wire
		// path, fallback/ deletion) is a follow-up slice.
		if s.anthropicProvider != nil && resolverErr == nil &&
			resolvedReq.Provider == adapterresolver.ProviderAnthropic {
			if registered, lookupErr := s.providerRegistry.Lookup(adapterresolver.ProviderAnthropic); lookupErr == nil {
				_ = registered
				// Intentional placeholder. We look up to assert
				// registry presence under the correct ID. Today
				// Execute returns ErrLegacyDispatchPath, so we still
				// fall through to the legacy dispatch path below.
			}
		}
		anthropicbackend.Dispatch(s, anthropicbackend.FallbackConfig{
			Enabled:            s.cfg.Fallback.Enabled,
			Trigger:            s.cfg.Fallback.Trigger,
			ForwardToShunt:     s.cfg.Fallback.ForwardToShunt.Enabled,
			ForwardToShuntName: s.cfg.Fallback.ForwardToShunt.Shunt,
			FailureEscalation:  s.cfg.Fallback.FailureEscalation,
		}, w, r, req, model, effort, reqID, body)
		return
	case BackendCodex:
		if s.codexProvider == nil {
			writeError(w, http.StatusServiceUnavailable, "codex_disabled",
				"codex backend is not enabled in [adapter.codex]")
			return
		}
		if resolverErr != nil || resolvedReq.Provider != adapterresolver.ProviderCodex {
			writeError(w, http.StatusBadRequest, "unresolved_codex",
				"resolver did not map this request to the codex provider")
			return
		}
		s.dispatchCodexProvider(w, r, req, model, reqID, cursorReq, resolvedReq)
		return
	default:
		writeError(w, http.StatusBadRequest, "unsupported_backend",
			"model resolved to unsupported backend "+model.Backend+"; configure anthropic, codex, shunt, or fallback explicitly")
		return
	}
}
