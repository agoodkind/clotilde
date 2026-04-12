package cmd

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/daemon"
	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Start the background daemon (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log := openLog("daemon")
			return daemon.Run(log)
		},
	}
}

// openLog returns a slog.Logger that writes JSON to the unified XDG state
// log file at ~/.local/state/clotilde/clotilde.jsonl.
// The component field distinguishes daemon vs wrapper entries.
func openLog(component string) *slog.Logger {
	logPath := filepath.Join(config.DefaultStateDir(), "clotilde.jsonl")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return slog.New(slog.NewJSONHandler(f, nil)).With("component", component)
}
