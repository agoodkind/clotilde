// Inject text into a running claude session via the daemon's
// SendToSession RPC. Useful for scripting and for testing the inject
// pipeline without the dashboard.
//
//   clotilde send <session> "hello from a script"
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/daemon"
)

func newSendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "send <session> <text>",
		Short: "Inject text into a running --remote-control session",
		Long: `Send text to a running Claude Code session as if it were typed at the
prompt. Requires the session to have been launched with --remote-control
so the wrapper opened its inject socket.

Multi word text is concatenated with spaces.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			text := joinArgs(args[1:])
			store, err := globalStore()
			if err != nil {
				return err
			}
			sess, err := store.Get(name)
			if err != nil || sess == nil {
				return fmt.Errorf("session %q not found", name)
			}
			delivered, derr := daemon.SendToSessionViaDaemon(context.Background(), sess.Metadata.SessionID, text)
			if derr != nil {
				return fmt.Errorf("daemon send: %w", derr)
			}
			if !delivered {
				return fmt.Errorf("no inject listener for %q (session not running, or launched without --remote-control)", name)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "delivered.")
			return nil
		},
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
