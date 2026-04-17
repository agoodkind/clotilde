package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/ui"
)

// Lipgloss styles for compact CLI output. These drive the colored headers
// and the bordered stats box; everything else stays plain text so that the
// output remains grep-friendly when piped to a file.
var (
	compactStyleHeader = lipgloss.NewStyle().
				Foreground(lipgloss.Color("75")).Bold(true)
	compactStyleMuted = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
	compactStyleGood = lipgloss.NewStyle().
				Foreground(lipgloss.Color("114")).Bold(true)
	compactStyleWarn = lipgloss.NewStyle().
				Foreground(lipgloss.Color("222")).Bold(true)
	compactStyleBad = lipgloss.NewStyle().
				Foreground(lipgloss.Color("204")).Bold(true)
	compactStyleLabel = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).Bold(true)
	compactStyleBox = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color("238")).
				Padding(0, 1)
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

// kv pretty-prints "label value" with consistent alignment and colour.
func kv(label, value string) string {
	return compactStyleLabel.Render(fmt.Sprintf("%-11s", label)) + value
}

// printStats prints a single snapshot inside a styled stats block.
func printStats(w io.Writer, label string, s transcriptStats) {
	heading := compactStyleHeader.Render(label + ":")
	body := fmt.Sprintf("%s %s %s %s %s %s %s %s %s %s",
		compactStyleMuted.Render("lines"), fmt.Sprintf("%-6d", s.TotalLines),
		compactStyleMuted.Render("chain"), fmt.Sprintf("%-6d", s.ChainLines),
		compactStyleMuted.Render("bytes"), fmt.Sprintf("%-10s", fmtBytes(s.Bytes)),
		compactStyleMuted.Render("tokens"), fmt.Sprintf("%-8s", fmtTokens(s.Tokens)),
		compactStyleMuted.Render("boundaries"), fmt.Sprintf("%d", s.Boundaries))
	fmt.Fprintf(w, "%-10s %s\n", heading, body)
}

// printDelta prints a before vs after summary line with signed, coloured deltas.
// Savings (negative tokens/bytes) render green; growth renders red.
func printDelta(w io.Writer, before, after transcriptStats) {
	dLines := after.TotalLines - before.TotalLines
	dChain := after.ChainLines - before.ChainLines
	dBytes := after.Bytes - before.Bytes
	dTokens := after.Tokens - before.Tokens
	pct := 0.0
	if before.Tokens > 0 {
		pct = float64(dTokens) / float64(before.Tokens) * 100
	}
	delta := compactStyleHeader.Render("Δ         ")
	paintInt := func(n int, prefix string) string {
		s := fmt.Sprintf("%s%+d", prefix, n)
		if n < 0 {
			return compactStyleGood.Render(s)
		}
		if n > 0 {
			return compactStyleBad.Render(s)
		}
		return compactStyleMuted.Render(s)
	}
	paintBytes := func() string {
		s := fmtBytesSigned(dBytes)
		if dBytes < 0 {
			return compactStyleGood.Render(s)
		}
		if dBytes > 0 {
			return compactStyleBad.Render(s)
		}
		return compactStyleMuted.Render(s)
	}
	paintTokens := func() string {
		s := fmtTokensSigned(dTokens)
		if dTokens < 0 {
			return compactStyleGood.Render(s)
		}
		if dTokens > 0 {
			return compactStyleBad.Render(s)
		}
		return compactStyleMuted.Render(s)
	}
	pctStyle := compactStyleMuted
	if pct < 0 {
		pctStyle = compactStyleGood
	} else if pct > 0 {
		pctStyle = compactStyleBad
	}
	fmt.Fprintf(w, "%s %s %s %s %s %s %s %s %s %s\n",
		delta,
		compactStyleMuted.Render("lines"), paintInt(dLines, ""),
		compactStyleMuted.Render("chain"), paintInt(dChain, ""),
		compactStyleMuted.Render("bytes"), paintBytes(),
		compactStyleMuted.Render("tokens"), paintTokens(),
		pctStyle.Render(fmt.Sprintf("(%+.1f%%)", pct)))
}

// printHeader renders the session heading block at the top of a compact run.
func printHeader(w io.Writer, sessionName, path string, dryRun bool) {
	line := func(label, value string) string {
		return compactStyleMuted.Render(fmt.Sprintf("%-12s", label)) + value
	}
	body := line("Session:", compactStyleHeader.Render(sessionName)) + "\n" +
		line("Transcript:", compactStyleMuted.Render(path))
	if dryRun {
		body += "\n" + line("Mode:", compactStyleWarn.Render("DRY RUN  no writes"))
	}
	fmt.Fprintln(w, compactStyleBox.Render(body))
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
// printPreview renders the post-boundary message preview in a compact,
// terminal-friendly format. Long bodies are truncated to one line and
// inline framing tags (task-id, tool-use-id, output-file, etc.) are
// stripped so the user sees the actual prose. Without these guards
// the dump is a wall of unreadable XML soup that obscures the chain
// changes the command is trying to communicate.
func printPreview(w io.Writer, heading string, previews []transcript.PreviewMessage) {
	if len(previews) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", heading)
	fmt.Fprintln(w, strings.Repeat("-", runeLen(heading)))
	for i, p := range previews {
		ts := "    --    "
		if !p.Timestamp.IsZero() {
			ts = p.Timestamp.Local().Format("01-02 15:04")
		}
		text := flattenPreviewText(p.Text)
		if len(text) > 100 {
			text = text[:97] + "..."
		}
		fmt.Fprintf(w, "  %2d. %s  %s\n", i+1, ts, text)
	}
	fmt.Fprintln(w)
}

// flattenPreviewText collapses the message body into a single readable
// line. Common framing markers from Claude Code (task-notification,
// task-id, tool-use-id, output-file, image references) are condensed to
// short stand-ins because their full form is noisy and never carries
// signal that a human reading the preview cares about.
func flattenPreviewText(s string) string {
	if s == "" {
		return "(empty)"
	}
	s = previewTagRe.ReplaceAllString(s, "")
	s = previewImageRe.ReplaceAllString(s, "[image]")
	s = previewWhitespaceRe.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "(framing only)"
	}
	return s
}

func runeLen(s string) int { return len([]rune(s)) }

var (
	previewTagRe        = regexp.MustCompile(`(?is)<(task-notification|task-id|tool-use-id|output-file|user-prompt-submit-hook|local-command[^>]*|command-name|command-message|command-args|system-reminder)\b[^>]*>.*?</[^>]+>|<[^>]+/>|</?[a-z][\w-]*[^>]*>`)
	previewImageRe      = regexp.MustCompile(`(?i)\[image[^\]]*\]`)
	previewWhitespaceRe = regexp.MustCompile(`\s+`)
)

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
			keepLastImages, _ := cmd.Flags().GetInt("keep-last-images")
			keepLastTools, _ := cmd.Flags().GetInt("keep-last-tool-results")
			keepLastThink, _ := cmd.Flags().GetInt("keep-last-thinking")
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

			// Header (bordered box + coloured tokens)
			printHeader(out, sess.Name, path, dryRun)
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
					StripToolResults:    stripResults,
					StripThinking:       stripThinking,
					StripImages:         stripImages,
					StripLargeBytes:     stripLarge,
					StripBefore:         stripBefore,
					KeepLast:            keepLast,
					KeepLastImages:      keepLastImages,
					KeepLastToolResults: keepLastTools,
					KeepLastThinking:    keepLastThink,
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
	cmd.Flags().Int("keep-last-images", 0, "Preserve the last N image blocks even when --strip-images is on")
	cmd.Flags().Int("keep-last-tool-results", 0, "Preserve the last N tool_result bodies even when --strip-tool-results is on")
	cmd.Flags().Int("keep-last-thinking", 0, "Preserve the last N thinking blocks even when --strip-thinking is on")
	cmd.Flags().Int("move-boundary", 0, "Move boundary so N%% of chain is visible (1-100)")
	cmd.Flags().Bool("remove-last-boundary", false, "Remove the most recent compact boundary")

	return cmd
}

// applyCompactChoices executes a compact with the given UI-generated choices.
// Called from the main TUI's compact form. Mirrors the CLI pipeline: backup,
// strip, then move boundary. Writes are skipped when DryRun is set.
// Returns a CompactResult so the TUI can display what changed without
// scraping stdout.
func applyCompactChoices(sess *session.Session, c ui.CompactChoices) (ui.CompactResult, error) {
	res := ui.CompactResult{
		KeptLastImages:   c.KeepLastImages,
		KeptLastTools:    c.KeepLastToolResults,
		KeptLastThinking: c.KeepLastThinking,
	}
	path := sess.Metadata.TranscriptPath
	if path == "" {
		return res, fmt.Errorf("session has no transcript path")
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		return res, fmt.Errorf("transcript not found: %w", err)
	}
	res.BeforeBytes = beforeInfo.Size()
	if beforeChain, _, _, err := transcript.WalkChain(path); err == nil {
		res.BeforeChainLines = len(beforeChain)
	}
	if !c.DryRun {
		backup, err := transcript.BackupTranscript(path, sess.Name)
		if err != nil {
			return res, fmt.Errorf("pre-compact backup failed: %w", err)
		}
		res.BackupPath = backup.Path
	}

	// Strip phase.
	if c.StripToolResults || c.StripThinking || c.StripImages || c.StripLargeInputs {
		chain, _, all, err := transcript.WalkChain(path)
		if err != nil {
			return res, err
		}
		opts := transcript.CompactOptions{
			StripToolResults:    c.StripToolResults,
			StripThinking:       c.StripThinking,
			StripImages:         c.StripImages,
			KeepLastImages:      c.KeepLastImages,
			KeepLastToolResults: c.KeepLastToolResults,
			KeepLastThinking:    c.KeepLastThinking,
		}
		if c.StripLargeInputs {
			opts.StripLargeBytes = 1024
		}
		newLines, stats := transcript.StripContent(all, chain, opts)
		res.StrippedTotal += stats.Total()
		res.StrippedImages += stats.Images
		res.StrippedTools += stats.ToolResults
		res.StrippedThinking += stats.Thinking
		res.StrippedLargeIn += stats.LargeInputs
		if !c.DryRun {
			if err := writeLines(path, newLines); err != nil {
				return res, err
			}
		}
	}

	// Move boundary phase. The form's "Set boundary" checkbox gates
	// the entire repositioning step. Without UseBoundary the strip
	// flags above are the only thing the user wanted us to do.
	if c.UseBoundary && c.BoundaryPercent > 0 && c.BoundaryPercent < 100 {
		res.BoundaryMoved = true
		chain, _, all, err := transcript.WalkChain(path)
		if err != nil {
			return res, err
		}
		summary := "Conversation compacted."
		existing := transcript.FindBoundaries(all)
		if len(existing) > 0 {
			lastB := existing[len(existing)-1]
			if lastB+1 < len(all) {
				var s struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				}
				if json.Unmarshal([]byte(all[lastB+1]), &s) == nil && s.Message.Content != "" {
					summary = s.Message.Content
				}
			}
			if !c.DryRun {
				removed, rmErr := transcript.RemoveBoundary(all, lastB)
				if rmErr != nil {
					return res, fmt.Errorf("removing old boundary: %w", rmErr)
				}
				if err := writeLines(path, removed); err != nil {
					return res, err
				}
				chain, _, all, err = transcript.WalkChain(path)
				if err != nil {
					return res, err
				}
			}
		}
		targetStep := len(chain) - (len(chain) * c.BoundaryPercent / 100)
		if targetStep < 1 {
			targetStep = 1
		}
		newLines, insErr := transcript.InsertBoundary(all, chain, targetStep, summary)
		if insErr != nil {
			return res, fmt.Errorf("inserting boundary: %w", insErr)
		}
		if !c.DryRun {
			if err := writeLines(path, newLines); err != nil {
				return res, err
			}
		}
	}
	if afterInfo, statErr := os.Stat(path); statErr == nil {
		res.AfterBytes = afterInfo.Size()
	}
	if afterChain, _, _, err := transcript.WalkChain(path); err == nil {
		res.AfterChainLines = len(afterChain)
	}
	return res, nil
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
