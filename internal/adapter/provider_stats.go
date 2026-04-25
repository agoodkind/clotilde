package adapter

import (
	"context"
	"strings"

	"goodkind.io/clyde/internal/adapter/chatemit"
)

func providerName(model ResolvedModel, path string) string {
	switch model.Backend {
	case BackendAnthropic:
		return "anthropic-oauth"
	case BackendFallback:
		return "anthropic-fallback"
	case BackendCodex:
		if path == "app" {
			return "openai-codex-app"
		}
		return "openai-codex"
	case BackendShunt:
		name := strings.TrimSpace(model.Shunt)
		if name == "" {
			return "shunt-unknown"
		}
		return "shunt-" + name
	default:
		return "claude-cli"
	}
}

func (s *Server) emitRequestStarted(ctx context.Context, model ResolvedModel, path, reqID, modelID string, stream bool) {
	chatemit.LogStarted(s.log, ctx, s.deps.RequestEvents, chatemit.StartedAttrs{
		Provider:  providerName(model, path),
		Backend:   model.Backend,
		RequestID: reqID,
		Alias:     model.Alias,
		ModelID:   modelID,
		Stream:    stream,
	})
}

func (s *Server) emitRequestStreamOpened(ctx context.Context, model ResolvedModel, path, reqID, modelID string, stream bool) {
	chatemit.LogStreamOpened(s.log, ctx, s.deps.RequestEvents, chatemit.StreamOpenedAttrs{
		Provider:  providerName(model, path),
		Backend:   model.Backend,
		RequestID: reqID,
		Alias:     model.Alias,
		ModelID:   modelID,
		Stream:    stream,
	})
}
