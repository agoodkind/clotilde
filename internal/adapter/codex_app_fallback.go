package adapter

import (
	"context"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

func (s *Server) runCodexAppFallback(
	ctx context.Context,
	req ChatRequest,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	cctx, cancel := context.WithTimeout(ctx, s.codexAppFallbackTimeout())
	defer cancel()
	system, prompt := BuildPrompt(req.Messages)
	return adaptercodex.RunAppFallback(cctx, adaptercodex.AppFallbackConfig{
		Binary:         s.codexAppServerPath(),
		RequestID:      reqID,
		Model:          req.Model,
		Effort:         adaptercodex.EffectiveAppEffort(req),
		Summary:        adaptercodex.EffectiveAppSummary(req),
		SystemPrompt:   system,
		Prompt:         prompt,
		SanitizePrompt: sanitizeForUpstreamCache,
		StartRPC:       adaptercodex.StartRPC,
		Logger:         s.log,
	}, emit)
}
