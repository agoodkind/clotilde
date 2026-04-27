package adapter

import (
	"context"
	"path/filepath"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/mitm"
)

func (s *Server) runCodexAppFallback(
	ctx context.Context,
	req ChatRequest,
	reqID string,
	emit func(adapteropenai.StreamChunk) error,
) (codexRunResult, error) {
	cctx, cancel := context.WithTimeout(ctx, s.codexAppFallbackTimeout())
	defer cancel()
	system, prompt := BuildPrompt(req.Messages)
	var startEnv map[string]string
	if globalCfg, err := config.LoadGlobalOrDefault(); err == nil {
		if overlay, overlayErr := mitm.PrepareCodexOverlay(cctx, globalCfg.MITM, s.log, filepath.Dir(resolveCodexAuthFile(s.cfg.Codex.AuthFile))); overlayErr != nil {
			s.log.Warn("adapter.codex.app_fallback.mitm_overlay_failed", "err", overlayErr)
		} else if overlay != nil {
			startEnv = map[string]string{"CODEX_HOME": overlay.Home}
		}
	}
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
		StartRPCEnv:    startEnv,
		Logger:         s.log,
		BodyLog: adaptercodex.BodyLogConfig{
			Mode:  s.logging.Body.Mode,
			MaxKB: s.logging.Body.MaxKB,
		},
	}, emit)
}
