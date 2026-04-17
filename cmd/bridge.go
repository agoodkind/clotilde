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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/daemon"
	"github.com/fgrehm/clotilde/internal/session"
)

func newBridgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bridge",
		Short: "Inspect remote control bridges",
	}
	cmd.AddCommand(newBridgeListCmd())
	cmd.AddCommand(newBridgeOpenCmd())
	cmd.AddCommand(newBridgeGhostsCmd())
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

// newBridgeGhostsCmd exposes `clotilde bridge ghosts`. The command
// classifies every file in ~/.claude/sessions/ into one of three
// buckets: alive (process still running and clotilde tracks the
// session), orphan (process dead, file lingering), or untracked
// (process alive but the session is not in the clotilde registry).
//
// The --clean flag removes orphan files so the daemon watcher stops
// reporting them. Server side bridge ghosts on claude.ai are not
// accessible from the CLI; the output includes a reminder to prune
// those via the mobile app or the web dashboard.
func newBridgeGhostsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ghosts",
		Short: "Classify local bridge files and optionally remove orphans",
		Long: `Walk ~/.claude/sessions/ and classify each pid/bridge file:

  alive      - process still running and the session is tracked by clotilde
  orphan     - process is dead but the bridge file lingers locally
  untracked  - process is alive but the session is not in the clotilde registry

Pass --clean to remove orphan files. The daemon watcher picks up the
deletion and emits BRIDGE_CLOSED events so connected dashboards refresh.

Server side bridge entries on claude.ai are not accessible through the
CLI. Prune those via the mobile app (long-press) or claude.ai/code.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clean, _ := cmd.Flags().GetBool("clean")
			ghosts, err := collectBridgeGhosts()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(ghosts) == 0 {
				fmt.Fprintln(out, "No local bridge files found.")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "STATE\tPID\tSESSION\tCWD\tBRIDGE\tFILE")
			removed := 0
			for _, g := range ghosts {
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n",
					g.State, g.PID, g.Name, g.CWD, g.Bridge, g.File)
				if clean && g.State == "orphan" {
					if err := os.Remove(g.File); err == nil {
						removed++
					}
				}
			}
			_ = w.Flush()
			if clean {
				fmt.Fprintf(out, "\nRemoved %d orphan file(s).\n", removed)
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, "Server side bridge entries on claude.ai are not accessible from the CLI.")
			fmt.Fprintln(out, "Prune those via the mobile app (long-press) or claude.ai/code.")
			return nil
		},
	}
	cmd.Flags().Bool("clean", false, "Delete orphan bridge files for dead processes")
	return cmd
}

// bridgeGhost is one classified bridge entry from the scan.
type bridgeGhost struct {
	State  string // alive | orphan | untracked
	PID    int
	Name   string
	CWD    string
	Bridge string
	File   string
}

// collectBridgeGhosts walks ~/.claude/sessions/, reads every pid
// file, and classifies the entry against both the running process
// table and the clotilde session store.
func collectBridgeGhosts() ([]bridgeGhost, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".claude", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	store, _ := session.NewGlobalFileStore()
	tracked := map[string]bool{}
	if store != nil {
		if all, err := store.List(); err == nil {
			for _, s := range all {
				if s.Metadata.SessionID != "" {
					tracked[s.Metadata.SessionID] = true
				}
				for _, prev := range s.Metadata.PreviousSessionIDs {
					if prev != "" {
						tracked[prev] = true
					}
				}
			}
		}
	}
	var out []bridgeGhost
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		var v struct {
			PID             int    `json:"pid"`
			SessionID       string `json:"sessionId"`
			Name            string `json:"name"`
			CWD             string `json:"cwd"`
			BridgeSessionID string `json:"bridgeSessionId"`
		}
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		state := "orphan"
		if v.PID > 0 && processAlive(v.PID) {
			if tracked[v.SessionID] {
				state = "alive"
			} else {
				state = "untracked"
			}
		}
		name := v.Name
		if name == "" {
			name = v.SessionID
		}
		out = append(out, bridgeGhost{
			State:  state,
			PID:    v.PID,
			Name:   name,
			CWD:    shortenCWDForGhost(v.CWD),
			Bridge: v.BridgeSessionID,
			File:   full,
		})
	}
	return out, nil
}

// processAlive returns true when sending signal 0 to pid succeeds,
// which is the cross-Unix way to check liveness without killing.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// shortenCWDForGhost collapses the home prefix for display.
func shortenCWDForGhost(cwd string) string {
	if cwd == "" {
		return "-"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return cwd
	}
	if cwd == home {
		return "~"
	}
	if strings.HasPrefix(cwd, home+"/") {
		return "~/" + cwd[len(home)+1:]
	}
	return cwd
}
