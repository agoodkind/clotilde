package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/session"
)

// isEphemeralWorkspace matches workspace roots that look like temp or test
// scratch paths. Mirrors internal/ui.isEphemeralSession so the two can
// be audited together. Kept private to cmd on purpose: we do not want
// this heuristic to become a cross-package contract.
func isEphemeralWorkspace(ws string) bool {
	if ws == "" {
		return false
	}
	prefixes := []string{
		"/private/var/folders/",
		"/var/folders/",
		"/tmp/",
		"/private/tmp/",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(ws, p) {
			return true
		}
	}
	return strings.Contains(ws, "/ginkgo")
}

func newPruneEphemeralCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune-ephemeral",
		Short: "Delete sessions rooted in temp directories (test leaks, tmp scratch)",
		Long: `Find and delete sessions whose workspace roots point inside a
system temp directory (/private/var/folders, /var/folders, /tmp) or any path
containing "/ginkgo". These are almost always leftovers from clotilde's own
go test runs, which currently use the global session store.

Examples:
  clotilde prune-ephemeral --dry-run   # list what would be deleted
  clotilde prune-ephemeral --yes       # delete without prompting`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			yes, _ := cmd.Flags().GetBool("yes")
			out := cmd.OutOrStdout()

			store, err := globalStore()
			if err != nil {
				return err
			}
			all, err := store.List()
			if err != nil {
				return fmt.Errorf("listing sessions: %w", err)
			}

			var targets []*session.Session
			for _, s := range all {
				if isEphemeralWorkspace(s.Metadata.WorkspaceRoot) {
					targets = append(targets, s)
				}
			}

			if len(targets) == 0 {
				fmt.Fprintln(out, "No ephemeral sessions found.")
				return nil
			}

			fmt.Fprintf(out, "Found %d ephemeral session(s):\n", len(targets))
			for _, s := range targets {
				fmt.Fprintf(out, "  %s  %s\n", s.Name, s.Metadata.WorkspaceRoot)
			}

			if dryRun {
				fmt.Fprintln(out, "\n[dry-run] No deletions performed.")
				return nil
			}

			if !yes {
				fmt.Fprintf(out, "\nDelete these %d sessions? [y/N]: ", len(targets))
				var answer string
				_, _ = fmt.Fscanln(cmd.InOrStdin(), &answer)
				answer = strings.ToLower(strings.TrimSpace(answer))
				if answer != "y" && answer != "yes" {
					fmt.Fprintln(out, "Cancelled.")
					return nil
				}
			}

			deleted := 0
			for _, s := range targets {
				if err := deleteSession(s, store); err != nil {
					fmt.Fprintf(out, "  FAILED %s: %v\n", s.Name, err)
					continue
				}
				deleted++
			}
			fmt.Fprintf(out, "\nDeleted %d of %d ephemeral sessions.\n", deleted, len(targets))
			return nil
		},
	}

	cmd.Flags().Bool("dry-run", false, "List matches without deleting")
	cmd.Flags().Bool("yes", false, "Skip the confirmation prompt")
	return cmd
}
