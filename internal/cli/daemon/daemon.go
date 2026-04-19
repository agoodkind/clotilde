package daemon

import (
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/cli"
	daemonsvc "goodkind.io/clyde/internal/daemon"
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
			log := slog.Default().With("component", "daemon")
			return daemonsvc.Run(log, pruneLoop(), oauthLoop())
		},
	}
}
