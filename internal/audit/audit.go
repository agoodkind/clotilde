// Package audit provides a shared slog.Logger that writes structured JSON
// to ~/.local/state/clyde/audit.jsonl. All processes (CLI, MCP server,
// daemon) write to the same file via rotating append writes.
package audit

import (
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/gklog"
	gklogversion "goodkind.io/gklog/version"
)

const auditFile = "audit.jsonl"

// NewLogger creates an slog.Logger that writes JSON to stderr and the audit
// log file with rotation. Returns the logger and a cleanup function.
func NewLogger(component string) (*slog.Logger, func()) {
	stateDir := config.DefaultStateDir()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return stderrFallback(component), func() {}
	}

	logPath := filepath.Join(stateDir, auditFile)
	jsonOpts := &slog.HandlerOptions{Level: slog.LevelDebug}

	lj := gklog.NewLumberjackWriterWithConfig(logPath, gklog.RotationConfig{
		MaxSizeMB:  5,
		MaxBackups: 0,
		MaxAgeDays: 0,
	})
	stderrH := slog.NewJSONHandler(os.Stderr, jsonOpts)
	fileH := slog.NewJSONHandler(lj, jsonOpts)
	logger := slog.New(gklog.NewTeeHandler(stderrH, fileH)).
		With(slog.String("build", gklogversion.String())).
		With("component", component)

	return logger, func() { _ = lj.Close() }
}

func stderrFallback(component string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})).With(slog.String("build", gklogversion.String())).With("component", component)
}
