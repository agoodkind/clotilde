package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"goodkind.io/clyde/internal/cli"
	"goodkind.io/clyde/internal/config"
	daemonsvc "goodkind.io/clyde/internal/daemon"
	"goodkind.io/gklog"
)

func NewCmd(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Start the background daemon (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slog.Info("cli.daemon.invoked",
				"component", "cli",
				"version", f.Build.Version,
			)
			log, closeLog := openLog("daemon")
			defer closeLog()
			return daemonsvc.Run(log, pruneLoop(), oauthLoop())
		},
	}
}

func openLog(component string) (*slog.Logger, func()) {
	logPath := filepath.Join(config.DefaultStateDir(), "clyde.jsonl")
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
