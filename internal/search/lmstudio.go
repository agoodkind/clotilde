package search

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// loadedModel represents a model from `lms ps --json`.
type loadedModel struct {
	Identifier   string `json:"identifier"`
	ModelKey     string `json:"modelKey"`
	Path         string `json:"path"`
	SizeBytes    int64  `json:"sizeBytes"`
	ContextLen   int    `json:"contextLength"`
	Status       string `json:"status"`
	LastUsedTime *int64 `json:"lastUsedTime"`
}

// matchesModel checks if a loaded model matches the given base name,
// comparing against Identifier, ModelKey, and Path.
func matchesModel(m loadedModel, base string) bool {
	return baseModelName(m.ModelKey) == base ||
		baseModelName(m.Identifier) == base ||
		baseModelName(m.Path) == base
}

// baseModelName strips the publisher prefix (e.g. "qwen/qwen3-coder-next"
// becomes "qwen3-coder-next") so we match regardless of namespace.
func baseModelName(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}

// ensureModelLoaded loads a model via lms, with memory-aware eviction.
// Does not unload --all. Instead, checks if the model is already loaded,
// evicts LRU idle models if needed to stay within memory budget, then loads.
// maxMemGB of 0 means no memory budget enforcement.
func ensureModelLoaded(ctx context.Context, model string, contextLen int, maxMemGB int) error {
	lms, err := exec.LookPath("lms")
	if err != nil {
		return nil // lms not installed, skip
	}

	log := slog.Default()

	loaded, err := listLoaded(ctx, lms)
	if err != nil {
		loaded = nil
	}

	// Already loaded with sufficient context? Done.
	base := baseModelName(model)
	for _, m := range loaded {
		if matchesModel(m, base) && (contextLen == 0 || m.ContextLen >= contextLen) {
			log.Info("model already loaded", "model", model, "context", m.ContextLen)
			return nil
		}
	}

	// Unload this specific model if loaded with insufficient context.
	for _, m := range loaded {
		if matchesModel(m, base) {
			log.Info("unloading model for context upgrade", "model", m.Identifier, "had", m.ContextLen, "need", contextLen)
			_ = exec.CommandContext(ctx, lms, "unload", m.Identifier).Run()
		}
	}

	// Estimate size of model to load.
	newSize := estimateModelSize(ctx, lms, model)

	// Evict idle models if we'd exceed the memory budget.
	maxMemBytes := int64(maxMemGB) * 1024 * 1024 * 1024
	if maxMemBytes > 0 && newSize > 0 {
		loaded, _ = listLoaded(ctx, lms) // refresh after potential unload
		evictForBudget(ctx, lms, loaded, base, newSize, maxMemBytes)
	}

	// Load the model
	log.Info("loading model", "model", model, "context_length", contextLen, "estimated_size_gb", newSize/(1024*1024*1024))
	loadCtx, loadCancel := context.WithTimeout(ctx, 120*time.Second)
	defer loadCancel()

	args := []string{"load", model, "-y"}
	if contextLen > 0 {
		args = []string{"load", model, "-c", fmt.Sprintf("%d", contextLen), "-y"}
	}
	cmd := exec.CommandContext(loadCtx, lms, args...)
	if output, loadErr := cmd.CombinedOutput(); loadErr != nil {
		return fmt.Errorf("lms load %s: %w\n%s", model, loadErr, output)
	}

	log.Info("model loaded", "model", model)
	return nil
}

// listLoaded returns the currently loaded models via `lms ps --json`.
func listLoaded(ctx context.Context, lms string) ([]loadedModel, error) {
	out, err := exec.CommandContext(ctx, lms, "ps", "--json").Output()
	if err != nil {
		return nil, err
	}
	var models []loadedModel
	if err := json.Unmarshal(out, &models); err != nil {
		return nil, err
	}
	return models, nil
}

// estimateModelSize gets the size of a model from `lms ls --json`.
func estimateModelSize(ctx context.Context, lms string, model string) int64 {
	out, err := exec.CommandContext(ctx, lms, "ls", "--json").Output()
	if err != nil {
		return 0
	}
	base := baseModelName(model)
	var models []struct {
		ModelKey  string `json:"modelKey"`
		SizeBytes int64  `json:"sizeBytes"`
	}
	if err := json.Unmarshal(out, &models); err != nil {
		return 0
	}
	for _, m := range models {
		if baseModelName(m.ModelKey) == base {
			return m.SizeBytes
		}
	}
	return 0
}

// evictForBudget unloads idle models (LRU first) until there's room for newSize
// within maxMem. Never evicts the model we're about to load or actively generating models.
func evictForBudget(ctx context.Context, lms string, loaded []loadedModel, keepBase string, newSize, maxMem int64) {
	log := slog.Default()

	var totalLoaded int64
	for _, m := range loaded {
		totalLoaded += m.SizeBytes
	}

	needed := totalLoaded + newSize - maxMem
	if needed <= 0 {
		return // fits within budget
	}

	log.Info("memory budget exceeded, evicting idle models",
		"total_loaded_gb", totalLoaded/(1024*1024*1024),
		"new_size_gb", newSize/(1024*1024*1024),
		"budget_gb", maxMem/(1024*1024*1024),
		"need_to_free_gb", needed/(1024*1024*1024),
	)

	// Sort candidates: idle models first, then by last used time (oldest first).
	candidates := make([]loadedModel, 0, len(loaded))
	for _, m := range loaded {
		if matchesModel(m, keepBase) {
			continue // don't evict the model we're loading
		}
		if m.Status == "generating" {
			continue // don't evict actively generating models
		}
		candidates = append(candidates, m)
	}

	sort.Slice(candidates, func(i, j int) bool {
		ti, tj := int64(0), int64(0)
		if candidates[i].LastUsedTime != nil {
			ti = *candidates[i].LastUsedTime
		}
		if candidates[j].LastUsedTime != nil {
			tj = *candidates[j].LastUsedTime
		}
		return ti < tj // oldest first
	})

	for _, m := range candidates {
		if needed <= 0 {
			break
		}
		log.Info("evicting model", "model", m.Identifier, "size_gb", m.SizeBytes/(1024*1024*1024))
		_ = exec.CommandContext(ctx, lms, "unload", m.Identifier).Run()
		needed -= m.SizeBytes
	}
}
