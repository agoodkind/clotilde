// Per session remote control flag plumbing for the CLI commands.
// The flag value gets persisted into the session's settings.json via
// the daemon so claude --remote-control fires automatically on the
// next invocation. CLI flags --remote-control and --no-remote-control
// override anything the profile or global default sets.
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/daemon"
	"github.com/fgrehm/clotilde/internal/session"
)

// applyRemoteControlFlag reads the --remote-control and
// --no-remote-control flags from cmd. When set it routes the change
// through the daemon. Sessions without either flag keep whatever
// value they already had.
func applyRemoteControlFlag(cmd *cobra.Command, sess *session.Session) error {
	if cmd == nil || sess == nil || sess.Name == "" {
		return nil
	}
	enable, _ := cmd.Flags().GetBool("remote-control")
	disable, _ := cmd.Flags().GetBool("no-remote-control")
	if !enable && !disable {
		return nil
	}
	if enable && disable {
		return fmt.Errorf("--remote-control and --no-remote-control are mutually exclusive")
	}
	value := enable
	ok, err := daemon.UpdateSessionRemoteControlViaDaemon(context.Background(), sess.Name, value)
	if err != nil && !ok {
		return err
	}
	return nil
}
