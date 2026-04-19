package daemon

import (
	"context"
	"log/slog"

	"github.com/spf13/cobra"
)

// NewOAuthRefreshCmd returns a hidden one-shot OAuth refresh for launchd; see
// docs/launchd/io.goodkind.clyde.oauth-refresh.plist.example.
func NewOAuthRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "oauth-refresh",
		Short:  "Refresh Anthropic OAuth token once",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			RunOAuthRefreshOnce(context.Background(), slog.Default())
			return nil
		},
	}
}
