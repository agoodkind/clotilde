package search

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ensureModelLoaded unloads all models and loads the specified one.
// No-op if lms CLI is not on PATH.
func ensureModelLoaded(ctx context.Context, model string) error {
	lms, err := exec.LookPath("lms")
	if err != nil {
		return nil // lms not installed, skip
	}

	// Check if already loaded
	out, err := exec.CommandContext(ctx, lms, "ps").Output()
	if err == nil && isModelLoaded(string(out), model) {
		return nil
	}

	// Unload everything first to free memory
	unloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_ = exec.CommandContext(unloadCtx, lms, "unload", "--all").Run()

	// Load the requested model
	loadCtx, loadCancel := context.WithTimeout(ctx, 120*time.Second)
	defer loadCancel()
	cmd := exec.CommandContext(loadCtx, lms, "load", model, "-y")
	if output, loadErr := cmd.CombinedOutput(); loadErr != nil {
		return fmt.Errorf("lms load %s: %w\n%s", model, loadErr, output)
	}

	return nil
}

// isModelLoaded checks if the model appears in `lms ps` output.
// Strips namespace prefix for matching (e.g. "qwen/qwen3-coder-next" matches "qwen3-coder-next").
func isModelLoaded(psOutput, model string) bool {
	bare := model
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		bare = model[idx+1:]
	}
	return strings.Contains(psOutput, model) || strings.Contains(psOutput, bare)
}
