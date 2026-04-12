package cmd

import (
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
)

// newExecCmd returns a hidden subcommand used by the `claude` shell wrapper.
// It acquires a per-session settings file from the daemon (starting the daemon
// if needed), execs the real claude with --settings injected, then releases
// the session. No session metadata is written; this is purely a model-isolation
// shim for bare claude invocations that don't go through clotilde sessions.
func newExecCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "exec",
		Short:  "Exec claude with per-session model isolation (internal, used by shell wrapper)",
		Hidden: true,
		// Pass all remaining arguments through to claude unchanged.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return claude.Exec(args)
		},
	}
}
