package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/session"
)

// emptyPruneOptions controls how prune-empty decides whether a session is
// abandoned. The defaults match the values used in the conservative audit
// that shipped with this command.
type emptyPruneOptions struct {
	MaxLines   int           // only prune transcripts with fewer than this many lines
	MinAge     time.Duration // only prune sessions whose transcript has not been modified in this long
	RequireNo  bool          // only prune when the session has zero real (non-synthetic) assistant turns
	IncludeNil bool          // also prune sessions whose transcriptPath is missing
}

// defaultEmptyPrune returns the conservative defaults used by the command.
// The criteria intentionally require multiple signals so no live session
// accidentally gets swept.
func defaultEmptyPrune() emptyPruneOptions {
	return emptyPruneOptions{
		MaxLines:   25,
		MinAge:     24 * time.Hour,
		RequireNo:  true,
		IncludeNil: true,
	}
}

// findEmptySessions walks the session store and returns sessions that look
// abandoned per opts. "Abandoned" means: the user started a session but never
// got a real model response recorded, and nobody has touched the session
// since.
func findEmptySessions(store session.Store, opts emptyPruneOptions) ([]*session.Session, []string, error) {
	all, err := store.List()
	if err != nil {
		return nil, nil, err
	}
	var hits []*session.Session
	var reasons []string
	cutoff := time.Now().Add(-opts.MinAge)

	for _, s := range all {
		tp := s.Metadata.TranscriptPath
		if tp == "" || !fileExists(tp) {
			if opts.IncludeNil {
				hits = append(hits, s)
				reasons = append(reasons, "no transcript")
			}
			continue
		}
		info, err := os.Stat(tp)
		if err != nil {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}

		lines, realAssistant := countTranscript(tp)
		if lines >= opts.MaxLines {
			continue
		}
		if opts.RequireNo && realAssistant > 0 {
			continue
		}
		hits = append(hits, s)
		reasons = append(reasons,
			fmt.Sprintf("lines=%d asst=%d age=%s",
				lines, realAssistant,
				shortDuration(time.Since(info.ModTime()))))
	}
	return hits, reasons, nil
}

// countTranscript returns the total line count and the number of assistant
// entries whose model is a real model (not "<synthetic>").
func countTranscript(path string) (lines, realAssistant int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		lines++
		var e struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Type == "assistant" && e.Message.Model != "" && e.Message.Model != "<synthetic>" {
			realAssistant++
		}
	}
	return lines, realAssistant
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// shortDuration renders a duration in hours or days for human readability.
func shortDuration(d time.Duration) string {
	h := int(d.Hours())
	if h < 48 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd", h/24)
}

func newPruneEmptyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune-empty",
		Short: "Delete sessions with no real assistant response and no recent activity",
		Long: `Find and delete sessions that look abandoned. A session qualifies when:

  - its transcript is missing, OR
  - it has fewer than --max-lines entries AND no real (non-synthetic)
    assistant response AND has not been touched in at least --min-age.

These are usually sessions that were started and immediately exited, or
sessions where the API call failed before any response landed. Defaults
are conservative so a live or recently active session is never matched.

Examples:
  clotilde prune-empty --dry-run               # preview
  clotilde prune-empty --yes                   # delete without prompting
  clotilde prune-empty --min-age 72h --yes     # only 3+ day idle
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			yes, _ := cmd.Flags().GetBool("yes")
			maxLines, _ := cmd.Flags().GetInt("max-lines")
			minAgeStr, _ := cmd.Flags().GetString("min-age")

			opts := defaultEmptyPrune()
			if maxLines > 0 {
				opts.MaxLines = maxLines
			}
			if minAgeStr != "" {
				d, err := time.ParseDuration(minAgeStr)
				if err != nil {
					return fmt.Errorf("invalid --min-age: %w", err)
				}
				opts.MinAge = d
			}

			store, err := globalStore()
			if err != nil {
				return err
			}
			hits, reasons, err := findEmptySessions(store, opts)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(hits) == 0 {
				fmt.Fprintln(out, "No empty sessions found.")
				return nil
			}

			fmt.Fprintf(out, "Found %d empty session(s):\n", len(hits))
			for i, s := range hits {
				fmt.Fprintf(out, "  %-32s  %s\n", s.Name, reasons[i])
			}
			if dryRun {
				fmt.Fprintln(out, "\n[dry-run] No deletions performed.")
				return nil
			}
			if !yes {
				fmt.Fprintf(out, "\nDelete these %d sessions? [y/N]: ", len(hits))
				var ans string
				_, _ = fmt.Fscanln(cmd.InOrStdin(), &ans)
				if !strings.EqualFold(strings.TrimSpace(ans), "y") && !strings.EqualFold(strings.TrimSpace(ans), "yes") {
					fmt.Fprintln(out, "Cancelled.")
					return nil
				}
			}
			deleted := 0
			for _, s := range hits {
				if err := deleteSession(s, store); err != nil {
					fmt.Fprintf(out, "  FAIL %s: %v\n", s.Name, err)
					continue
				}
				deleted++
			}
			fmt.Fprintf(out, "\nDeleted %d of %d empty sessions.\n", deleted, len(hits))
			return nil
		},
	}
	cmd.Flags().Bool("dry-run", false, "List matches without deleting")
	cmd.Flags().Bool("yes", false, "Skip the confirmation prompt")
	cmd.Flags().Int("max-lines", 25, "Only prune transcripts with fewer than this many lines")
	cmd.Flags().String("min-age", "24h", "Only prune sessions whose transcript has not been modified in at least this duration (e.g. 24h, 7d)")
	return cmd
}
