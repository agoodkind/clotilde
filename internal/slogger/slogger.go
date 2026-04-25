// Package slogger is the clyde-wide structured logging facade.
//
// It is a thin wrapper around goodkind.io/gklog (the cross-repo logging
// package). Request scoped loggers on context use goodkind.io/gklog
// (WithLogger, LoggerFromContext). Every call site uses Go's
// standard log/slog package directly; this package only handles initialization
// (Setup).
//
// The standard is non-negotiable: every operation in the codebase MUST
// emit at least one slog event. Free-form fmt.Println / log.Printf are
// rejected by `make slog-audit`. See docs/SLOG.md for the full spec.
package slogger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/gklog"
	"goodkind.io/gklog/version"
)

const (
	envOverride       = "CLYDE_SLOG_PATH"
	defaultBaseSubdir = "clyde"
	defaultTUIFile    = "clyde-tui.jsonl"
	defaultDaemonFile = "clyde-daemon.jsonl"
)

// ProcessRole identifies which process family is initializing slog.
type ProcessRole string

const (
	ProcessRoleTUI    ProcessRole = "tui"
	ProcessRoleDaemon ProcessRole = "daemon"
)

// Setup initializes the global slog logger via gklog. It writes
// JSONL to a process-specific path under $XDG_STATE_HOME/clyde
// (or [logging.paths] when configured). Stdout logging is disabled
// so command output remains machine-friendly
// for CLI callers. Call once at process start before emitting any events;
// otherwise slog.Default falls back to a stderr text handler.
//
// Returns an io.Closer that the caller must Close on shutdown so the
// rotating file handles flush. closer.Close() is safe to call once.
func Setup(cfg config.LoggingConfig, role ProcessRole) (io.Closer, error) {
	level := strings.ToLower(strings.TrimSpace(cfg.Level))
	switch level {
	case "debug", "info", "warn", "error":
	case "":
		return nopCloser{}, fmt.Errorf("slogger: logging.level required, must be one of debug|info|warn|error, got %q", level)
	default:
		return nopCloser{}, fmt.Errorf("slogger: logging.level required, must be one of debug|info|warn|error, got %q", level)
	}

	path := defaultPath(cfg, role)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nopCloser{}, fmt.Errorf("slogger: mkdir %s: %w", filepath.Dir(path), err)
	}
	rotationEnabled := true
	if cfg.Rotation.Enabled != nil {
		rotationEnabled = *cfg.Rotation.Enabled
	}
	if !rotationEnabled {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nopCloser{}, fmt.Errorf("slogger: open json log file %s: %w", path, err)
		}
		logger := slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: parseJSONMinLevel(level)}))
		slog.SetDefault(logger.With("build", version.String()))
		return file, nil
	}
	// stdout is reserved for command output (so CLI subcommands like
	// `clyde compact clone-for-test --print-name` produce machine-
	// parseable single-line output). slog goes to the rotated JSONL
	// file at the resolved process path; tail that file for
	// live diagnostics.
	compress := cfg.Rotation.Compress
	if compress == nil {
		compress = boolPtr(true)
	}
	logger, closer, err := gklog.New(gklog.Config{
		JSONLogFile:   path,
		DisableStdout: true,
		JSONMinLevel:  level,
		Rotation: gklog.RotationConfig{
			MaxSizeMB:  cfg.Rotation.MaxSizeMB,
			MaxBackups: cfg.Rotation.MaxBackups,
			MaxAgeDays: cfg.Rotation.MaxAgeDays,
			Compress:   compress,
		},
	})
	if err != nil {
		return nopCloser{}, fmt.Errorf("slogger: gklog.New: %w", err)
	}
	slog.SetDefault(logger)
	return closer, nil
}

func boolPtr(v bool) *bool {
	return &v
}

func parseJSONMinLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

// defaultPath resolves the process-aware JSONL path. Honors the env
// override for tests. Operators may set [logging.paths] to override
// the per-role defaults.
func defaultPath(cfg config.LoggingConfig, role ProcessRole) string {
	if p := os.Getenv(envOverride); p != "" {
		return p
	}
	if role == ProcessRoleDaemon && cfg.Paths.Daemon != "" {
		return cfg.Paths.Daemon
	}
	if role == ProcessRoleTUI && cfg.Paths.TUI != "" {
		return cfg.Paths.TUI
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), defaultBaseSubdir, fileForRole(role))
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, defaultBaseSubdir, fileForRole(role))
}

func fileForRole(role ProcessRole) string {
	if role == ProcessRoleDaemon {
		return defaultDaemonFile
	}
	return defaultTUIFile
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
