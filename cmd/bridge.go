// Bridge inspection CLI commands. Subcommands:
//
//   clotilde bridge ls
//   clotilde bridge open <session>
//
// The list command queries the daemon for the current bridge map.
// The open command resolves the session name to a Claude session UUID
// and invokes "open" on the bridge URL.
package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/daemon"
)

func newBridgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bridge",
		Short: "Inspect remote control bridges",
	}
	cmd.AddCommand(newBridgeListCmd())
	cmd.AddCommand(newBridgeOpenCmd())
	return cmd
}

func newBridgeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List active claude --remote-control bridges",
		RunE: func(cmd *cobra.Command, args []string) error {
			bridges, err := daemon.ListBridgesViaDaemon(context.Background())
			if err != nil {
				return fmt.Errorf("daemon ListBridges: %w", err)
			}
			if len(bridges) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No active bridges.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION\tPID\tBRIDGE\tURL")
			for _, b := range bridges {
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", b.SessionId, b.Pid, b.BridgeSessionId, b.Url)
			}
			return w.Flush()
		},
	}
}

func newBridgeOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <session>",
		Short: "Open the bridge URL for a tracked session in the browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			store, err := globalStore()
			if err != nil {
				return err
			}
			sess, err := store.Get(name)
			if err != nil || sess == nil {
				return fmt.Errorf("session %q not found", name)
			}
			bridges, err := daemon.ListBridgesViaDaemon(context.Background())
			if err != nil {
				return err
			}
			for _, b := range bridges {
				if b.SessionId == sess.Metadata.SessionID {
					fmt.Fprintln(cmd.OutOrStdout(), b.Url)
					return exec.Command("open", b.Url).Start()
				}
			}
			return fmt.Errorf("no active bridge for session %q (run /remote-control or relaunch with --remote-control)", name)
		},
	}
}
