// Package audit provides a shared slog.Logger that writes structured JSON
// to ~/.local/state/clotilde/audit.jsonl. All processes (CLI, MCP server,
// daemon) write to the same file via append-only opens.
package audit

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fgrehm/clotilde/internal/config"
)

const auditFile = "audit.jsonl"

// NewLogger creates an slog.Logger that writes JSON to both stderr and the
// audit log file. Returns the logger and a cleanup function.
func NewLogger(component string) (*slog.Logger, func()) {
	stateDir := config.DefaultStateDir()
	_ = os.MkdirAll(stateDir, 0o700)

	logPath := filepath.Join(stateDir, auditFile)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		// Fall back to stderr-only
		return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})).With("component", component), func() {}
	}

	w := io.MultiWriter(os.Stderr, f)
	logger := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})).With("component", component)

	return logger, func() { _ = f.Close() }
}

// LogPath returns the path to the audit log file.
func LogPath() string {
	return filepath.Join(config.DefaultStateDir(), auditFile)
}
