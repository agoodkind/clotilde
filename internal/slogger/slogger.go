// Package slogger is the clyde-wide structured logging facade.
//
// It is a thin wrapper around goodkind.io/gklog (the cross-repo logging
// package) plus the request-scoped context.WithLogger pattern adopted
// from tack/internal/telemetry. Every call site uses Go's standard
// log/slog package directly; this package only handles initialization
// (Setup) and context plumbing (WithLogger / L).
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

	"goodkind.io/gklog"
)

const (
	envOverride       = "CLYDE_SLOG_PATH"
	defaultBaseSubdir = "clyde"
	defaultFile       = "clyde.jsonl"
)

// Setup initialises the global slog logger via gklog. It writes
// JSONL to $XDG_STATE_HOME/clyde/clyde.jsonl (rotated) AND to
// stdout for journald/launchd capture. Call once at process start
// before emitting any events; otherwise slog.Default falls back to a
// stderr text handler.
//
// Returns an io.Closer that the caller must Close on shutdown so the
// rotating file handles flush. closer.Close() is safe to call once.
func Setup(level string) (io.Closer, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "debug", "info", "warn", "error":
	case "":
		return nopCloser{}, fmt.Errorf("slogger: logging.level required, must be one of debug|info|warn|error, got %q", level)
	default:
		return nopCloser{}, fmt.Errorf("slogger: logging.level required, must be one of debug|info|warn|error, got %q", level)
	}

	path := defaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nopCloser{}, fmt.Errorf("slogger: mkdir %s: %w", filepath.Dir(path), err)
	}
	// stdout is reserved for command output (so CLI subcommands like
	// `clyde compact clone-for-test --print-name` produce machine-
	// parseable single-line output). slog goes to the rotated JSONL
	// file at $XDG_STATE_HOME/clyde/clyde.jsonl; tail that file
	// for live diagnostics.
	logger, closer, err := gklog.New(gklog.Config{
		JSONLogFile:   path,
		DisableStdout: true,
		JSONMinLevel:  level,
	})
	if err != nil {
		return nopCloser{}, fmt.Errorf("slogger: gklog.New: %w", err)
	}
	slog.SetDefault(logger)
	return closer, nil
}

// defaultPath resolves the unified JSONL path. Honors the env override
// for tests; otherwise lands at $XDG_STATE_HOME/clyde/clyde.jsonl
// to mirror the daemon's existing log location.
func defaultPath() string {
	if p := os.Getenv(envOverride); p != "" {
		return p
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), defaultBaseSubdir, defaultFile)
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, defaultBaseSubdir, defaultFile)
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
