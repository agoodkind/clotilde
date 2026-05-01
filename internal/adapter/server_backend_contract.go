package adapter

import (
	"log/slog"
	"net/http"
	"strings"

	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
	"goodkind.io/clyde/internal/correlation"
)

// applyBackendOverride keeps backend selection in the root adapter so request
// decode, model resolution, and final backend choice stay readable in one
// place before control passes to a backend package.
func (s *Server) applyBackendOverride(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, reqID string) (ResolvedModel, bool) {
	override := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Clyde-Backend")))
	if override == "" {
		return model, true
	}

	original := model.Backend
	switch override {
	case BackendAnthropic, BackendShunt, BackendCodex:
		model.Backend = override
	default:
		writeError(w, http.StatusBadRequest, "invalid_backend_override",
			"X-Clyde-Backend must be one of: anthropic, shunt, codex")
		return model, false
	}

	attrs := []slog.Attr{
		slog.String("request_id", reqID),
		slog.String("alias", req.Model),
		slog.String("from", original),
		slog.String("to", override),
	}
	attrs = append(attrs, correlation.AttrsFromContext(r.Context())...)
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.backend.overridden", attrs...)
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
	attrs := []slog.Attr{
		slog.String("request_id", reqID),
		slog.String("alias", req.Model),
		slog.String("backend", model.Backend),
		slog.String("resolved_model", model.ClaudeModel),
		slog.String("effort", effort),
		slog.Bool("stream", req.Stream),
	}
	attrs = append(attrs, correlation.AttrsFromContext(r.Context())...)
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.backend.dispatching", attrs...)
	switch model.Backend {
	case BackendShunt:
		s.forwardShunt(w, r, model, body)
		return
	case BackendAnthropic:
		if s.anthropicProvider == nil {
			writeError(w, http.StatusServiceUnavailable, "anthropic_disabled",
				"anthropic backend is not enabled in [adapter]")
			return
		}
		if resolverErr != nil || resolvedReq.Provider != adapterresolver.ProviderAnthropic {
			writeError(w, http.StatusBadRequest, "unresolved_anthropic",
				"resolver did not map this request to the anthropic provider")
			return
		}
		if err := s.dispatchAnthropicProvider(w, r, effort, reqID, resolvedReq); err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
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
			"model resolved to unsupported backend "+model.Backend+"; configure anthropic, codex, or shunt explicitly")
		return
	}
}
