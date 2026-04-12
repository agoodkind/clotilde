package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// findRealClaude locates the actual claude binary, skipping any agent-gate
// wrapper that may be installed earlier in PATH.
func findRealClaude() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine own path: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", fmt.Errorf("failed to resolve own path: %w", err)
	}
	selfDir := filepath.Dir(self)

	path := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "claude")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		// Skip if this resolves back to ourselves (wrapper installed as claude).
		if resolved == self || filepath.Dir(resolved) == selfDir {
			continue
		}
		// Skip if the candidate is another agent-gate binary (check binary name).
		base := strings.ToLower(filepath.Base(resolved))
		if base == "agent-gate" {
			continue
		}
		return candidate, nil
	}

	return "", fmt.Errorf("real claude binary not found in PATH (is it installed?)")
}
