package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/ui"
)

// transcriptStats captures the shape of a transcript at a point in time.
type transcriptStats struct {
	TotalLines   int
	ChainLines   int
	Bytes        int64
	Tokens       int // approximate, via tiktoken cl100k with 1.20x multiplier
	Boundaries   int
}

// snapshotStats reads the file at path, walks the chain, and returns a snapshot.
func snapshotStats(path string) (transcriptStats, error) {
	var s transcriptStats
	info, err := os.Stat(path)
	if err != nil {
		return s, fmt.Errorf("stat transcript: %w", err)
	}
	s.Bytes = info.Size()

	chain, _, all, err := transcript.WalkChain(path)
	if err != nil {
		return s, fmt.Errorf("walking chain: %w", err)
	}
	s.TotalLines = len(all)
	s.ChainLines = len(chain)
	s.Boundaries = len(transcript.FindBoundaries(all))

	tokens, tokErr := transcript.EstimateTokens(all, chain)
	if tokErr == nil {
		s.Tokens = tokens
	}
	return s, nil
}

// printStats prints a single snapshot under a label.
func printStats(w io.Writer, label string, s transcriptStats) {
	fmt.Fprintf(w, "%-8s lines=%-6d chain=%-6d bytes=%-10s tokens=%-8s boundaries=%d\n",
		label+":", s.TotalLines, s.ChainLines, fmtBytes(s.Bytes), fmtTokens(s.Tokens), s.Boundaries)
}

// printDelta prints a summary line describing before -> after with signed deltas.
func printDelta(w io.Writer, before, after transcriptStats) {
	dLines := after.TotalLines - before.TotalLines
	dChain := after.ChainLines - before.ChainLines
	dBytes := after.Bytes - before.Bytes
	dTokens := after.Tokens - before.Tokens
	pct := 0.0
	if before.Tokens > 0 {
		pct = float64(dTokens) / float64(before.Tokens) * 100
	}
	fmt.Fprintf(w, "Δ        lines=%+d chain=%+d bytes=%s tokens=%s (%+.1f%%)\n",
		dLines, dChain, fmtBytesSigned(dBytes), fmtTokensSigned(dTokens), pct)
}

func fmtBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	}
	return fmt.Sprintf("%.2fMB", float64(n)/(1024*1024))
}

func fmtBytesSigned(n int64) string {
	sign := "+"
	if n < 0 {
		sign = "-"
		n = -n
	}
	return sign + fmtBytes(n)
}

func fmtTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
}

func fmtTokensSigned(n int) string {
	sign := "+"
	if n < 0 {
		sign = "-"
		n = -n
	}
	return sign + fmtTokens(n)
}

// previewCount is how many user-message previews to show in compact output.
const previewCount = 10

// printPreview writes a numbered list of previews with a short absolute
// timestamp (YYYY-MM-DD HH:MM) alongside each message.
func printPreview(w io.Writer, heading string, previews []transcript.PreviewMessage) {
	if len(previews) == 0 {
		return
	}
	fmt.Fprintf(w, "%s\n", heading)
	for i, p := range previews {
		ts := "     --     "
		if !p.Timestamp.IsZero() {
			ts = p.Timestamp.Local().Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %2d. [%s] %s\n", i+1, ts, p.Text)
	}
}

func newCompactCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compact <session>",
		Short: "Advanced compaction of a session transcript",
		Long: `Compact a session transcript by moving the compaction boundary and
optionally stripping tool results, thinking blocks, or large inputs.

This manipulates the Claude Code JSONL transcript directly, controlling
what the LLM sees in its context window on the next resume.

Examples:
  clotilde compact my-session --move-boundary 50
  clotilde compact my-session --strip-tool-results --keep-last 200
  clotilde compact my-session --strip-thinking
  clotilde compact my-session --strip-images
  clotilde compact my-session --strip-large 1000 --dry-run
  clotilde compact my-session --remove-last-boundary`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			out := cmd.OutOrStdout()

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
			stripThinking, _ := cmd.Flags().GetBool("strip-thinking")
			stripImages, _ := cmd.Flags().GetBool("strip-images")
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

			// Initial snapshot
			before, err := snapshotStats(path)
			if err != nil {
				return err
			}

			// When no action flags are set and stdout is a TTY, launch the interactive UI.
			noActionFlags := !dryRun && !stripResults && !stripThinking && !stripImages && stripLarge == 0 && keepLast == 0 &&
				stripBeforeStr == "" && moveBoundary == 0 && !removeLast
			if noActionFlags && isatty.IsTerminal(os.Stdout.Fd()) {
				chain, _, all, walkErr := transcript.WalkChain(path)
				if walkErr != nil {
					return fmt.Errorf("walking chain: %w", walkErr)
				}
				choices, tuiErr := ui.RunCompactUI(sess.Name, path, chain, all)
				if tuiErr != nil {
					return fmt.Errorf("compact UI: %w", tuiErr)
				}
				if choices.Cancelled || (!choices.Applied && !choices.DryRun) {
					fmt.Fprintln(out, "Cancelled.")
					return nil
				}
				moveBoundary = choices.BoundaryPercent
				stripResults = choices.StripToolResults
				stripThinking = choices.StripThinking
				if choices.StripLargeInputs {
					stripLarge = 1024
				}
				dryRun = choices.DryRun
			}

			// Header
			fmt.Fprintf(out, "Session:    %s\n", sess.Name)
			fmt.Fprintf(out, "Transcript: %s\n", path)
			if dryRun {
				fmt.Fprintln(out, "Mode:       DRY RUN (no writes)")
			}
			printStats(out, "Before", before)

			// Existing boundaries for context
			_, _, allLines, err := transcript.WalkChain(path)
			if err != nil {
				return err
			}
			boundaries := transcript.FindBoundaries(allLines)
			if len(boundaries) > 0 {
				fmt.Fprintf(out, "Boundaries: %d @ lines %v\n", len(boundaries), boundaries)
			}
			fmt.Fprintln(out)

			willWrite := !dryRun && (stripResults || stripThinking || stripImages || stripLarge > 0 || moveBoundary > 0 || removeLast)
			if willWrite {
				bk, bkErr := transcript.BackupTranscript(path, sess.Name)
				if bkErr != nil {
					return fmt.Errorf("pre-compact backup failed (refusing to proceed): %w", bkErr)
				}
				fmt.Fprintf(out, "Backup:     %s (%s)\n\n", bk.Path, fmtBytes(bk.Bytes))
			}

			// --- Action: remove last boundary ---
			if removeLast {
				if len(boundaries) < 1 {
					fmt.Fprintln(out, "No boundary to remove.")
					return nil
				}
				lastBoundary := boundaries[len(boundaries)-1]
				fmt.Fprintf(out, "Removing boundary at line %d...\n", lastBoundary)

				if dryRun {
					fmt.Fprintln(out, "[dry-run] Would remove boundary and reconnect chain.")
					return nil
				}

				chain, _, all, _ := transcript.WalkChain(path)
				newLines, removeErr := transcript.RemoveBoundary(all, lastBoundary)
				if removeErr != nil {
					return fmt.Errorf("removing boundary: %w", removeErr)
				}
				if writeErr := writeLines(path, newLines); writeErr != nil {
					return writeErr
				}

				after, _ := snapshotStats(path)
				printStats(out, "After", after)
				printDelta(out, before, after)

				afterChain, _, afterAll, _ := transcript.WalkChain(path)
				preview := transcript.PreviewMessages(afterAll, afterChain, 0, previewCount)
				_ = chain
				fmt.Fprintln(out)
				printPreview(out, fmt.Sprintf("First %d user messages after removal:", len(preview)), preview)
				return nil
			}

			// --- Action: strip content ---
			if stripResults || stripThinking || stripImages || stripLarge > 0 {
				opts := transcript.CompactOptions{
					StripToolResults: stripResults,
					StripThinking:    stripThinking,
					StripImages:      stripImages,
					StripLargeBytes:  stripLarge,
					StripBefore:      stripBefore,
					KeepLast:         keepLast,
				}
				fmt.Fprintf(out, "Stripping (tool_results=%v, thinking=%v, images=%v, large>%d bytes, keep_last=%d)...\n",
					stripResults, stripThinking, stripImages, stripLarge, keepLast)

				chain, _, all, _ := transcript.WalkChain(path)
				newLines, stats := transcript.StripContent(all, chain, opts)
				fmt.Fprintf(out, "Stripped:   %d blocks total (tool_results=%d thinking=%d images=%d large_inputs=%d)\n",
					stats.Total(), stats.ToolResults, stats.Thinking, stats.Images, stats.LargeInputs)

				if dryRun {
					fmt.Fprintln(out, "[dry-run] No writes performed.")
					return nil
				}
				if writeErr := writeLines(path, newLines); writeErr != nil {
					return writeErr
				}

				after, _ := snapshotStats(path)
				printStats(out, "After", after)
				printDelta(out, before, after)

				// If --move-boundary is also set, fall through to apply it.
				if moveBoundary == 0 {
					return nil
				}
				fmt.Fprintln(out)
				// Re-read for the next step
				before = after
			}

			// --- Action: move boundary ---
			if moveBoundary > 0 {
				if moveBoundary > 100 {
					return fmt.Errorf("--move-boundary must be 1-100 (percent of chain visible)")
				}

				// Get summary text from existing boundary before removing it.
				summaryText := "Conversation compacted."
				chain, _, all, _ := transcript.WalkChain(path)
				existingBoundaries := transcript.FindBoundaries(all)
				if len(existingBoundaries) > 0 {
					lastB := existingBoundaries[len(existingBoundaries)-1]
					if lastB+1 < len(all) {
						var summary struct {
							Message struct {
								Content string `json:"content"`
							} `json:"message"`
						}
						if json.Unmarshal([]byte(all[lastB+1]), &summary) == nil && summary.Message.Content != "" {
							summaryText = summary.Message.Content
						}
					}
					fmt.Fprintf(out, "Removing existing boundary at line %d before repositioning...\n", lastB)
					if !dryRun {
						removed, removeErr := transcript.RemoveBoundary(all, lastB)
						if removeErr != nil {
							return fmt.Errorf("removing old boundary: %w", removeErr)
						}
						if writeErr := writeLines(path, removed); writeErr != nil {
							return writeErr
						}
						chain, _, all, err = transcript.WalkChain(path)
						if err != nil {
							return fmt.Errorf("re-walking chain: %w", err)
						}
					}
				}

				targetStep := len(chain) - (len(chain) * moveBoundary / 100)
				if targetStep < 1 {
					targetStep = 1
				}
				visibleCount := len(chain) - targetStep
				fmt.Fprintf(out, "Moving boundary to %d%% visible (%d entries)...\n", moveBoundary, visibleCount)

				// Preview lines after the proposed boundary
				preview := transcript.PreviewMessages(all, chain, targetStep, previewCount)
				if len(preview) > 0 {
					printPreview(out, fmt.Sprintf("First %d user messages after new boundary:", len(preview)), preview)
					fmt.Fprintln(out)
				}

				if dryRun {
					// Compute a hypothetical "after" using just the chain slice.
					afterChain := chain[targetStep:]
					tokens, _ := transcript.EstimateTokens(all, afterChain)
					hypothetical := transcriptStats{
						TotalLines: len(all),
						ChainLines: len(afterChain),
						Bytes:      before.Bytes, // no write
						Tokens:     tokens,
						Boundaries: len(existingBoundaries),
					}
					printStats(out, "After*", hypothetical)
					printDelta(out, before, hypothetical)
					fmt.Fprintln(out, "[dry-run] No writes performed. (After* reflects projected chain only.)")
					return nil
				}

				newLines, insertErr := transcript.InsertBoundary(all, chain, targetStep, summaryText)
				if insertErr != nil {
					return fmt.Errorf("inserting boundary: %w", insertErr)
				}
				if writeErr := writeLines(path, newLines); writeErr != nil {
					return writeErr
				}
				after, _ := snapshotStats(path)
				printStats(out, "After", after)
				printDelta(out, before, after)
				return nil
			}

			// No action was taken (and no TUI triggered); just print stats and exit.
			return nil
		},
	}

	cmd.Flags().Bool("dry-run", false, "Show what would change without writing")
	cmd.Flags().Bool("strip-tool-results", false, "Replace tool results with stubs")
	cmd.Flags().Bool("strip-thinking", false, "Remove assistant thinking blocks")
	cmd.Flags().Bool("strip-images", false, "Remove image blocks (fixes 'image exceeds dimension limit' on resume)")
	cmd.Flags().Int("strip-large", 0, "Strip tool results/inputs larger than N bytes")
	cmd.Flags().String("strip-before", "", "Only strip entries before this time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().Int("keep-last", 0, "Keep last N chain entries fully intact")
	cmd.Flags().Int("move-boundary", 0, "Move boundary so N%% of chain is visible (1-100)")
	cmd.Flags().Bool("remove-last-boundary", false, "Remove the most recent compact boundary")

	return cmd
}

func writeLines(path string, lines []string) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("writing line: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("syncing file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("closing file: %w", err)
	}
	return os.Rename(tmp, path)
}
