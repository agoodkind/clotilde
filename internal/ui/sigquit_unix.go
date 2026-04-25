//go:build unix

package ui

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"time"

	"goodkind.io/clyde/internal/config"
)

func installSIGQUITDumpHandler() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGQUIT)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-done:
				return
			case <-ch:
				path, err := writeSIGQUITDump()
				if err != nil {
					slog.Error("tui.signal.sigquit.dump_failed",
						"component", "tui",
						"err", err)
					_, _ = fmt.Fprintf(os.Stderr, "clyde SIGQUIT goroutine dump failed: %v\n", err)
					continue
				}
				slog.Error("tui.signal.sigquit.dump_written",
					"component", "tui",
					"path", path)
				_, _ = fmt.Fprintf(os.Stderr, "clyde SIGQUIT goroutine dump written to %s\n", path)
			}
		}
	}()

	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func writeSIGQUITDump() (string, error) {
	stateDir := config.DefaultStateDir()
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir state dir: %w", err)
	}

	var buf bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&buf, 2); err != nil {
		return "", fmt.Errorf("write goroutine profile: %w", err)
	}

	path := filepath.Join(stateDir, fmt.Sprintf("sigquit-goroutines-%s.txt", time.Now().UTC().Format("20060102T150405.000000000Z")))
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		return "", fmt.Errorf("write dump file: %w", err)
	}
	return path, nil
}
