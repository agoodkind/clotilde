package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/gklog"
	"goodkind.io/lmctl"
)

func embeddingModelID(cfg config.SearchLocal) string {
	if cfg.EmbeddingModel != "" {
		return cfg.EmbeddingModel
	}
	return defaultEmbeddingModel
}

// ensureEmbeddingModelReady loads the embedding weights the search pipeline
// needs. When [config.SearchLocal.EmbeddingURL] is set, embeddings are
// served by that broker (for example lmd) and this path POSTs
// /swiftlmd/preload instead of using lmctl against LM Studio.
func ensureEmbeddingModelReady(ctx context.Context, cfg config.SearchLocal) error {
	model := embeddingModelID(cfg)
	log := gklog.LoggerFromContext(ctx).With("component", "search", "subcomponent", "embed")
	log.InfoContext(ctx, "search.embed.ensure_ready.invoked",
		"model", model,
		"has_url", cfg.EmbeddingURL != "",
	)
	if strings.TrimSpace(cfg.EmbeddingURL) != "" {
		return preloadLmdEmbedding(ctx, cfg, model)
	}
	start := time.Now()
	err := lmctl.EnsureLoaded(ctx, model, lmctl.WithMaxMemoryGB(cfg.MaxMemoryGB))
	if err != nil {
		log.ErrorContext(ctx, "search.embed.ensure_ready.failed",
			"model", model,
			"duration_ms", time.Since(start).Milliseconds(),
			"err", err,
		)
		return err
	}
	log.InfoContext(ctx, "search.embed.ensure_ready.completed",
		"model", model,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return nil
}

func preloadLmdEmbedding(ctx context.Context, cfg config.SearchLocal, model string) error {
	log := gklog.LoggerFromContext(ctx).With("component", "search", "subcomponent", "embed")
	log.InfoContext(ctx, "search.embed.preload_lmd.invoked", "model", model)
	base := cfg.ResolvedEmbeddingURL()
	if base == "" {
		log.ErrorContext(ctx, "search.embed.preload_lmd.failed", "model", model)
		return fmt.Errorf("search.local embedding_url and url are both empty")
	}
	u := base + "/swiftlmd/preload"
	payload, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := cfg.ResolvedEmbeddingToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		log.ErrorContext(ctx, "search.embed.preload_lmd.failed",
			"model", model,
			"duration_ms", time.Since(started).Milliseconds(),
			"err", err,
		)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.WarnContext(ctx, "search.embed.preload_lmd.failed",
			"model", model,
			"status_code", resp.StatusCode,
		)
		return fmt.Errorf("preload embedding: HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	log.InfoContext(ctx, "search.embed.preload_lmd.completed",
		"model", model,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return nil
}
