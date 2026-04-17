package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/session"
)

// newPruneAutonameCmd ships `clotilde prune-autoname`. The command walks
// every Claude Code project directory and removes transcripts that come
// from clotilde's own sdk-cli auto-name calls. Those transcripts have
// entrypoint=sdk-cli and a kebab-case naming prompt as their first
// queue-operation entry, so they are easy to identify with a single
// header read. Removing them keeps the native `claude --resume` picker
// from filling up with one-off helper invocations.
func newPruneAutonameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune-autoname",
		Short: "Delete Claude Code transcripts left behind by clotilde's auto-name calls",
		Long: `clotilde dispatches one-off claude -p invocations to a small model when it
needs to suggest a session name. Each invocation creates a transcript file
under ~/.claude/projects/<encoded-cwd>/ that pollutes the native
claude --resume picker. This command finds those transcripts via their
sdk-cli entrypoint marker and removes them.

The walk skips anything that is already tracked by a clotilde session, so
real conversations are never touched. Use --dry-run to preview before
deleting.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			minAgeStr, _ := cmd.Flags().GetString("min-age")
			minAge := time.Hour
			if minAgeStr != "" {
				d, err := time.ParseDuration(minAgeStr)
				if err != nil {
					return fmt.Errorf("invalid --min-age: %w", err)
				}
				minAge = d
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			projects := filepath.Join(home, ".claude", "projects")
			results, err := session.ScanProjects(projects)
			if err != nil {
				return err
			}

			store, err := globalStore()
			if err != nil {
				return err
			}
			knownPaths, err := buildKnownTranscriptPaths(store)
			if err != nil {
				return err
			}

			cutoff := time.Now().Add(-minAge)
			var matches []session.DiscoveryResult
			for _, r := range results {
				if !r.IsAutoName {
					continue
				}
				if knownPaths[r.TranscriptPath] {
					continue
				}
				fi, statErr := os.Stat(r.TranscriptPath)
				if statErr != nil {
					continue
				}
				if !fi.ModTime().Before(cutoff) {
					continue
				}
				matches = append(matches, r)
			}

			out := cmd.OutOrStdout()
			if len(matches) == 0 {
				fmt.Fprintln(out, "No auto-name transcripts to prune.")
				return nil
			}
			fmt.Fprintf(out, "Found %d auto-name transcript(s):\n", len(matches))
			for _, m := range matches {
				fmt.Fprintf(out, "  %s\n", m.TranscriptPath)
			}
			if dryRun {
				fmt.Fprintln(out, "\n[dry-run] No deletions performed.")
				return nil
			}

			deleted := 0
			for _, m := range matches {
				if err := os.Remove(m.TranscriptPath); err != nil {
					fmt.Fprintf(out, "  FAIL %s: %v\n", m.TranscriptPath, err)
					continue
				}
				deleted++
			}
			fmt.Fprintf(out, "\nDeleted %d of %d transcripts.\n", deleted, len(matches))
			return nil
		},
	}
	cmd.Flags().Bool("dry-run", false, "List matches without deleting")
	cmd.Flags().String("min-age", "1h", "Only prune transcripts older than this (e.g. 1h, 24h, 7d)")
	return cmd
}

// buildKnownTranscriptPaths returns the set of transcript paths that
// any tracked session points at. Both current and previous transcript
// references count so /clear chains are preserved.
func buildKnownTranscriptPaths(store session.Store) (map[string]bool, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(all))
	for _, s := range all {
		if s.Metadata.TranscriptPath != "" {
			out[s.Metadata.TranscriptPath] = true
		}
	}
	// Best-effort dedupe by basename so weird symlink mismatches do
	// not cause a known transcript to be deleted.
	for path := range out {
		base := filepath.Base(path)
		dir := filepath.Dir(path)
		alt := filepath.Join(dir, strings.TrimSpace(base))
		out[alt] = true
	}
	return out, nil
}
