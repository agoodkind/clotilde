package mcp

import (
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/cli"
	"goodkind.io/clyde/internal/mcpserver"
)

// NewCmd returns the hidden `clyde mcp` command that starts the MCP stdio server.
func NewCmd(f *cli.Factory) *cobra.Command {
	return &cobra.Command{
		Use:    "mcp",
		Short:  "Start MCP stdio server for Claude Code integration",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = f
			slog.Info("cli.mcp.invoked")
			return mcpserver.Serve(cmd.Context())
		},
	}
}
