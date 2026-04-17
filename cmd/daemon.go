package cmd

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/daemon"
	"github.com/spf13/cobra"
	"goodkind.io/gklog"
)

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Start the background daemon (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log, closeLog := openLog("daemon")
			defer closeLog()
			return daemon.Run(log)
		},
	}
}

// openLog returns a slog.Logger that writes JSON to the unified XDG state
// log file at ~/.local/state/clotilde/clotilde.jsonl, with rotation.
// The component field distinguishes daemon vs wrapper entries.
func openLog(component string) (*slog.Logger, func()) {
	logPath := filepath.Join(config.DefaultStateDir(), "clotilde.jsonl")
	noClose := func() {}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil)), noClose
	}
	inner, closer, err := gklog.New(gklog.Config{
		JSONLogFile:   logPath,
		Rotation:      gklog.RotationConfig{MaxSizeMB: 5, MaxBackups: 0, MaxAgeDays: 0},
		DisableStdout: true,
		JSONMinLevel:  "debug",
	})
	if err != nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil)), noClose
	}
	return inner.With("component", component), func() {
		if closer != nil {
			_ = closer.Close()
		}
	}
}
