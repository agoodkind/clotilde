package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/transcript"
)

func newCompactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compact <session>",
		Short: "Advanced compaction of a session transcript",
		Long: `Compact a session transcript by moving the compaction boundary and
optionally stripping tool results, large inputs, and thinking blocks.

This manipulates the Claude Code JSONL transcript directly, controlling
what the LLM sees in its context window on the next resume.

Examples:
  clotilde compact my-session --move-boundary 50
  clotilde compact my-session --strip-tool-results --keep-last 200
  clotilde compact my-session --strip-large 1000 --dry-run
  clotilde compact my-session --remove-last-boundary`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			store, err := globalStore()
			if err != nil {
				return err
			}

			sess, err := store.Resolve(name)
			if err != nil {
				return err
			}
			if sess == nil {
				return fmt.Errorf("session '%s' not found", name)
			}

			path := sess.Metadata.TranscriptPath
			if path == "" {
				return fmt.Errorf("session has no transcript path")
			}
			if _, statErr := os.Stat(path); statErr != nil {
				return fmt.Errorf("transcript not found: %s", path)
			}

			dryRun, _ := cmd.Flags().GetBool("dry-run")
			stripResults, _ := cmd.Flags().GetBool("strip-tool-results")
			stripLarge, _ := cmd.Flags().GetInt("strip-large")
			keepLast, _ := cmd.Flags().GetInt("keep-last")
			stripBeforeStr, _ := cmd.Flags().GetString("strip-before")
			moveBoundary, _ := cmd.Flags().GetInt("move-boundary")
			removeLast, _ := cmd.Flags().GetBool("remove-last-boundary")

			var stripBefore time.Time
			if stripBeforeStr != "" {
				var parseErr error
				stripBefore, parseErr = time.Parse(time.RFC3339, stripBeforeStr)
				if parseErr != nil {
					stripBefore, parseErr = time.Parse("2006-01-02", stripBeforeStr)
					if parseErr != nil {
						return fmt.Errorf("invalid --strip-before time: %s (use RFC3339 or YYYY-MM-DD)", stripBeforeStr)
					}
				}
			}

			// Walk the chain
			chainLines, _, allLines, err := transcript.WalkChain(path)
			if err != nil {
				return fmt.Errorf("walking chain: %w", err)
			}

			boundaries := transcript.FindBoundaries(allLines)
			fmt.Fprintf(cmd.OutOrStdout(), "Session: %s\n", sess.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "Transcript: %s\n", path)
			fmt.Fprintf(cmd.OutOrStdout(), "Total lines: %d\n", len(allLines))
			fmt.Fprintf(cmd.OutOrStdout(), "Chain length: %d entries\n", len(chainLines))
			fmt.Fprintf(cmd.OutOrStdout(), "Compact boundaries: %d\n", len(boundaries))
			for i, b := range boundaries {
				fmt.Fprintf(cmd.OutOrStdout(), "  %d. line %d\n", i+1, b)
			}
			fmt.Fprintln(cmd.OutOrStdout())

			// Remove last boundary
			if removeLast && len(boundaries) >= 2 {
				lastBoundary := boundaries[len(boundaries)-1]
				beforeTokens, _ := transcript.EstimateTokens(allLines, chainLines)
				fmt.Fprintf(cmd.OutOrStdout(), "Removing boundary at line %d...\n", lastBoundary)
				fmt.Fprintf(cmd.OutOrStdout(), "Before: %d chain entries, ~%dk tokens\n", len(chainLines), beforeTokens/1000)

				if dryRun {
					fmt.Fprintln(cmd.OutOrStdout(), "[dry-run] Would remove boundary and reconnect chain")
					return nil
				}
				newLines, removeErr := transcript.RemoveBoundary(allLines, lastBoundary)
				if removeErr != nil {
					return fmt.Errorf("removing boundary: %w", removeErr)
				}
				if writeErr := writeLines(path, newLines); writeErr != nil {
					return writeErr
				}

				// Show after stats
				afterChain, _, afterLines, _ := transcript.WalkChain(path)
				afterTokens, _ := transcript.EstimateTokens(afterLines, afterChain)
				fmt.Fprintf(cmd.OutOrStdout(), "After:  %d chain entries, ~%dk tokens\n", len(afterChain), afterTokens/1000)

				preview := transcript.PreviewMessages(afterLines, afterChain, 0, 5)
				fmt.Fprintln(cmd.OutOrStdout(), "\nFirst 5 user messages after new boundary:")
				for i, msg := range preview {
					fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, msg)
				}

				fmt.Fprintf(cmd.OutOrStdout(), "\nRemoved. New effective boundary: line %d\n", boundaries[len(boundaries)-2])
				return nil
			}

			// Strip content
			if stripResults || stripLarge > 0 {
				opts := transcript.CompactOptions{
					StripToolResults: stripResults,
					StripLargeBytes:  stripLarge,
					StripBefore:      stripBefore,
					KeepLast:         keepLast,
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Stripping content (tool_results=%v, large>%d, keep_last=%d)...\n",
					stripResults, stripLarge, keepLast)
				newLines, stripped := transcript.StripContent(allLines, chainLines, opts)
				fmt.Fprintf(cmd.OutOrStdout(), "Stripped %d blocks\n", stripped)

				if dryRun {
					fmt.Fprintln(cmd.OutOrStdout(), "[dry-run] Would write stripped content")
					return nil
				}
				if writeErr := writeLines(path, newLines); writeErr != nil {
					return writeErr
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Written.")

				// Re-walk chain after stripping for boundary placement
				chainLines, _, allLines, err = transcript.WalkChain(path)
				if err != nil {
					return fmt.Errorf("re-walking chain: %w", err)
				}
			}

			// Move boundary
			if moveBoundary > 0 {
				if moveBoundary > 100 {
					return fmt.Errorf("--move-boundary must be 1-100 (percent of chain visible)")
				}

				// Get summary text from existing boundary before removing it
				summaryText := "Conversation compacted."
				existingBoundaries := transcript.FindBoundaries(allLines)
				if len(existingBoundaries) > 0 {
					lastB := existingBoundaries[len(existingBoundaries)-1]
					if lastB+1 < len(allLines) {
						var summary struct {
							Message struct {
								Content string `json:"content"`
							} `json:"message"`
						}
						if json.Unmarshal([]byte(allLines[lastB+1]), &summary) == nil && summary.Message.Content != "" {
							summaryText = summary.Message.Content
						}
					}

					// Remove the last boundary first so we operate on the full chain
					fmt.Fprintf(cmd.OutOrStdout(), "Removing existing boundary at line %d before repositioning...\n", lastB)
					if !dryRun {
						newLines, removeErr := transcript.RemoveBoundary(allLines, lastB)
						if removeErr != nil {
							return fmt.Errorf("removing old boundary: %w", removeErr)
						}
						if writeErr := writeLines(path, newLines); writeErr != nil {
							return writeErr
						}
						// Re-walk the full chain after removal
						chainLines, _, allLines, err = transcript.WalkChain(path)
						if err != nil {
							return fmt.Errorf("re-walking chain after boundary removal: %w", err)
						}
						fmt.Fprintf(cmd.OutOrStdout(), "Chain after removal: %d entries\n", len(chainLines))
					}
				}

				targetStep := len(chainLines) - (len(chainLines) * moveBoundary / 100)
				if targetStep < 1 {
					targetStep = 1
				}
				visibleCount := len(chainLines) - targetStep
				fmt.Fprintf(cmd.OutOrStdout(), "Moving boundary to %d%% visible (%d entries)...\n", moveBoundary, visibleCount)

				// Show before/after stats
				beforeTokens, _ := transcript.EstimateTokens(allLines, chainLines)
				afterChain := chainLines[targetStep:]
				afterTokens, _ := transcript.EstimateTokens(allLines, afterChain)

				fmt.Fprintf(cmd.OutOrStdout(), "\nBefore: %d chain entries, ~%dk tokens\n", len(chainLines), beforeTokens/1000)
				fmt.Fprintf(cmd.OutOrStdout(), "After:  %d chain entries, ~%dk tokens\n", visibleCount, afterTokens/1000)
				fmt.Fprintf(cmd.OutOrStdout(), "\nFirst 5 user messages after new boundary:\n")
				preview := transcript.PreviewMessages(allLines, chainLines, targetStep, 5)
				for i, msg := range preview {
					fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, msg)
				}
				fmt.Fprintln(cmd.OutOrStdout())

				if dryRun {
					fmt.Fprintln(cmd.OutOrStdout(), "[dry-run] No changes written.")
					return nil
				}
				newLines, insertErr := transcript.InsertBoundary(allLines, chainLines, targetStep, summaryText)
				if insertErr != nil {
					return fmt.Errorf("inserting boundary: %w", insertErr)
				}
				if writeErr := writeLines(path, newLines); writeErr != nil {
					return writeErr
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Boundary placed. %d entries visible after boundary.\n", visibleCount)
			}

			return nil
		},
	}

	cmd.Flags().Bool("dry-run", false, "Show what would change without writing")
	cmd.Flags().Bool("strip-tool-results", false, "Replace tool results with stubs")
	cmd.Flags().Int("strip-large", 0, "Strip tool results/inputs larger than N bytes")
	cmd.Flags().String("strip-before", "", "Only strip entries before this time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().Int("keep-last", 0, "Keep last N chain entries fully intact")
	cmd.Flags().Int("move-boundary", 0, "Move boundary so N%% of chain is visible (1-100)")
	cmd.Flags().Bool("remove-last-boundary", false, "Remove the most recent compact boundary")

	return cmd
}

func writeLines(path string, lines []string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("writing line: %w", err)
		}
	}
	return nil
}
