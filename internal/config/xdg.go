package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "clotilde"

// DefaultStateDir returns the XDG-derived state directory for clotilde.
//
// Resolution:
//
//	$XDG_STATE_HOME/clotilde    (if $XDG_STATE_HOME is set)
//	~/.local/state/clotilde      (XDG spec default)
func DefaultStateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, appName)
}

// RuntimeDir returns a user-scoped runtime directory for the daemon socket.
// Uses XDG_RUNTIME_DIR if set, then TMPDIR, then a UID-scoped fallback.
func RuntimeDir() string {
	uid := os.Getuid()
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, appName)
	}
	if base := os.Getenv("TMPDIR"); base != "" {
		return filepath.Join(base, appName)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", appName, uid))
}

// DaemonSocketPath returns the Unix socket path for the clotilde daemon.
func DaemonSocketPath() string {
	return filepath.Join(RuntimeDir(), "daemon.sock")
}

// SessionRuntimeDir returns the runtime directory for a specific wrapper session.
func SessionRuntimeDir(wrapperID string) string {
	return filepath.Join(RuntimeDir(), "sessions", wrapperID)
}

// EnsureRuntimeDir creates the clotilde runtime directory with correct permissions.
// XDG spec requires 0700 for XDG_RUNTIME_DIR contents.
func EnsureRuntimeDir() error {
	dir := RuntimeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create runtime dir %s: %w", dir, err)
	}
	return nil
}
