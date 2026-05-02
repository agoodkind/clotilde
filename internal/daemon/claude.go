package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// cachedRealClaude memoizes the resolved binary path for the lifetime
// of the daemon process. Re-Stat'ing every dir on every context update
// triggered macOS TCC prompts when PATH happened to include a
// protected directory like ~/Downloads.
var (
	cachedRealClaudePath string
	cachedRealClaudeOnce sync.Once
)

// findRealClaude locates the actual claude binary. The result is
// cached so we only walk the candidate directories once per daemon
// process. Protected user directories (Downloads, Desktop, Documents)
// are skipped so the search never triggers a macOS TCC consent prompt.
func findRealClaude() (string, error) {
	cachedRealClaudeOnce.Do(func() {
		cachedRealClaudePath = locateRealClaude()
		if cachedRealClaudePath != "" {
			daemonLifecycleLog.Logger().Info("daemon.claude.resolve_binary.found",
				"component", "daemon",
				"subcomponent", "claude_path",
				"path", cachedRealClaudePath,
			)
		} else {
			daemonLifecycleLog.Logger().Error("daemon.claude.resolve_binary.not_found",
				"component", "daemon",
				"subcomponent", "claude_path",
				"err", "not_found",
			)
		}
	})
	if cachedRealClaudePath == "" {
		return "", fmt.Errorf("real claude binary not found in PATH (is it installed?)")
	}
	return cachedRealClaudePath, nil
}

func locateRealClaude() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	resolvedSelf, err := filepath.EvalSymlinks(self)
	if err != nil {
		return ""
	}
	selfDir := filepath.Dir(resolvedSelf)

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
		if isProtectedDir(dir, home) {
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
		if resolved == resolvedSelf || filepath.Dir(resolved) == selfDir {
			continue
		}
		return candidate
	}
	return ""
}

// isProtectedDir reports whether dir lives inside a macOS TCC-guarded
// area. Stat-ing a file inside one of these directories triggers a
// consent prompt for unsigned binaries, which the daemon hits
// repeatedly when PATH happens to contain any of them.
func isProtectedDir(dir, home string) bool {
	if home == "" {
		return false
	}
	for _, sub := range []string{"Downloads", "Desktop", "Documents", "Library"} {
		base := filepath.Join(home, sub)
		if dir == base || strings.HasPrefix(dir, base+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
