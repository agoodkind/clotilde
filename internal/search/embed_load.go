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
	if strings.TrimSpace(cfg.EmbeddingURL) != "" {
		return preloadLmdEmbedding(ctx, cfg, model)
	}
	return lmctl.EnsureLoaded(ctx, model, lmctl.WithMaxMemoryGB(cfg.MaxMemoryGB))
}

func preloadLmdEmbedding(ctx context.Context, cfg config.SearchLocal, model string) error {
	base := cfg.ResolvedEmbeddingURL()
	if base == "" {
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
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("preload embedding: HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
