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

	// Build a search list: PATH first, then well-known install
	// locations that launchd jobs may not inherit. The daemon runs
	// under launchd with a minimal PATH so we need to look in places
	// like ~/.local/bin and ~/.npm-global/bin explicitly.
	dirs := filepath.SplitList(os.Getenv("PATH"))
	home, _ := os.UserHomeDir()
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, "n", "bin"),
			filepath.Join(home, ".volta", "bin"),
		)
	}
	dirs = append(dirs,
		"/usr/local/bin",
		"/opt/homebrew/bin",
	)

	for _, dir := range dirs {
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
