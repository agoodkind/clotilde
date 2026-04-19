// Package hook implements the hidden clyde hook subcommands used by Claude Code.
package hook

import (
	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/cli"
)

// NewCmd returns the parent cobra command for `clyde hook` and its children.
func NewCmd(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Internal commands for Claude Code hooks",
		Hidden: true,
		Long: `Internal commands called by Claude Code's session hooks.
These commands are not intended for direct user invocation.`,
	}
	cmd.AddCommand(newSessionStartCmd(f))
	return cmd
}
