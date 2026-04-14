package cmd

import (
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/mcpserver"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "mcp",
		Short:  "Start MCP stdio server for Claude Code integration",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcpserver.Serve(cmd.Context())
		},
	}
}
