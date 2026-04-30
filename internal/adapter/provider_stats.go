package adapter

import (
	"context"
	"strings"

	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

func providerName(model ResolvedModel, path string) string {
	switch model.Backend {
	case BackendAnthropic:
		return "anthropic-oauth"
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
	adapterruntime.LogStarted(s.log, ctx, s.deps.RequestEvents, adapterruntime.StartedAttrs{
		Provider:  providerName(model, path),
		Backend:   model.Backend,
		RequestID: reqID,
		Alias:     model.Alias,
		ModelID:   modelID,
		Stream:    stream,
	})
}

func (s *Server) emitRequestStreamOpened(ctx context.Context, model ResolvedModel, path, reqID, modelID string, stream bool) {
	adapterruntime.LogStreamOpened(s.log, ctx, s.deps.RequestEvents, adapterruntime.StreamOpenedAttrs{
		Provider:  providerName(model, path),
		Backend:   model.Backend,
		RequestID: reqID,
		Alias:     model.Alias,
		ModelID:   modelID,
		Stream:    stream,
	})
}
