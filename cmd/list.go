package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/fgrehm/clotilde/internal/util"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List sessions",
		Long: `List all clotilde sessions, grouped by workspace.

Use --workspace to show only sessions for the current workspace.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wsOnly, _ := cmd.Flags().GetBool("workspace")

			store, err := globalStore()
			if err != nil {
				return err
			}

			var sessions []*session.Session
			if wsOnly {
				workspaceRoot, wsErr := config.FindProjectRoot()
				if wsErr != nil {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No sessions for this workspace.")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nCreate a session with:")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  clotilde start <session-name>")
					return nil
				}
				sessions, err = store.ListForWorkspace(workspaceRoot)
			} else {
				sessions, err = store.List()
			}
			if err != nil {
				return fmt.Errorf("failed to list sessions: %w", err)
			}

			if len(sessions) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nCreate a session with:")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  clotilde start <session-name>")
				return nil
			}

			return showStaticTable(cmd, sessions, store)
		},
	}
	cmd.Flags().BoolP("workspace", "w", false, "Show only current workspace sessions")
	cmd.Flags().Bool("all", false, "Show sessions from all workspaces (now the default)")
	_ = cmd.Flags().MarkHidden("all")
	return cmd
}

// showInteractiveTable displays sessions in an interactive TUI table with sorting
// If a session is selected, it returns the session. Otherwise returns nil.
func showInteractiveTable(sessions []*session.Session, store session.Store) (*session.Session, error) {
	// Build headers
	headers := []string{"Name", "Dir", "Model", "Created", "Last Used"}

	// Build rows (rows will be in same order as sessions array initially)
	var rows [][]string
	for _, sess := range sessions {
		model, lastUsed := extractModelAndLastUsed(sess, store)
		nameStr := formatSessionName(sess)
		rows = append(rows, []string{nameStr, shortWorkspacePath(sess.Metadata.WorkspaceRoot), model, util.FormatRelativeTime(sess.Metadata.Created), util.FormatRelativeTime(lastUsed)})
	}

	// Create and run interactive table
	fmt.Printf("Sessions (%d total)\n\n", len(sessions))
	table := ui.NewTable(headers, rows).WithSorting()
	selectedRow, err := ui.RunTable(table)
	if err != nil {
		return nil, err
	}

	// If cancelled or no selection, return nil
	if len(selectedRow) == 0 {
		return nil, nil
	}

	// Map the selected row back to the session by name (first column)
	selectedName := selectedRow[0]
	for _, sess := range sessions {
		if sess.Name == selectedName {
			return sess, nil
		}
	}

	return nil, nil
}

// showStaticTable displays sessions in a static text table (for scripts/pipes)
func showStaticTable(cmd *cobra.Command, sessions []*session.Session, store session.Store) error {
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Sessions (%d total):\n", len(sessions))

	table := tablewriter.NewWriter(cmd.OutOrStdout())
	table.Header("NAME", "DIR", "MODEL", "CREATED", "LAST USED")

	for _, sess := range sessions {
		model, lastUsed := extractModelAndLastUsed(sess, store)
		nameStr := formatSessionName(sess)
		_ = table.Append(nameStr, shortWorkspacePath(sess.Metadata.WorkspaceRoot), model, util.FormatRelativeTime(sess.Metadata.Created), util.FormatRelativeTime(lastUsed))
	}

	_ = table.Render()
	return nil
}

// extractModelAndLastUsed reads the transcript tail once, returning both the model
// family and the best "last used" time. More efficient than separate ExtractLastModel
// and LastTranscriptTime calls, which would each open and seek the file.
func extractModelAndLastUsed(sess *session.Session, store session.Store) (string, time.Time) {
	lastUsed := sess.Metadata.LastAccessed
	model := "-"

	if sess.Metadata.TranscriptPath != "" {
		m, ts := claude.ExtractModelAndLastTime(sess.Metadata.TranscriptPath)
		if m != "" {
			model = m
		}
		if ts.After(lastUsed) {
			lastUsed = ts
		}
	}

	// Fall back to requested model from settings (error is non-critical; no settings is common)
	if model == "-" {
		settings, _ := store.LoadSettings(sess.Name) //nolint:errcheck // missing settings file is expected
		if settings != nil && settings.Model != "" {
			model = settings.Model
		}
	}

	return model, lastUsed
}

// formatSessionName formats the session name with type suffix
func formatSessionName(sess *session.Session) string {
	name := sess.Name
	if sess.Metadata.IsForkedSession {
		name += " [fork]"
	}
	if sess.Metadata.IsIncognito {
		name += " [inc]"
	}
	return name
}

// richPreviewFunc returns a PreviewFunc that shows full session details
// with clear section separation and stats.
// statsCache is the PickerModel.StatsCache map, shared by reference so background
// computation results become visible as they arrive.
func richPreviewFunc(store session.Store, statsCache map[string]*transcript.CompactQuickStats) ui.PreviewFunc {
	return func(sess *session.Session) string {
		var lines []string
		sep := "----------------------------"

		// Header: session name + context summary
		lines = append(lines, sess.Name)
		if sess.Metadata.Context != "" {
			lines = append(lines, sess.Metadata.Context)
		}

		// Session info
		lines = append(lines, sep)
		model, _ := extractModelAndLastUsed(sess, store)
		lines = append(lines, fmt.Sprintf("Model:     %s", model))
		lines = append(lines, fmt.Sprintf("Workspace: %s", shortWorkspacePath(sess.Metadata.WorkspaceRoot)))
		if sess.Metadata.IsForkedSession {
			lines = append(lines, fmt.Sprintf("Type:      fork of %s", sess.Metadata.ParentSession))
		}

		// Stats
		lines = append(lines, sep)
		lines = append(lines, fmt.Sprintf("Created:   %s", sess.Metadata.Created.Format("2006-01-02 15:04")))
		lines = append(lines, fmt.Sprintf("Last used: %s", util.FormatRelativeTime(sess.Metadata.LastAccessed)))
		if sess.Metadata.TranscriptPath != "" {
			if info, err := os.Stat(sess.Metadata.TranscriptPath); err == nil {
				sizeMB := float64(info.Size()) / (1024 * 1024)
				lines = append(lines, fmt.Sprintf("Transcript: %.1f MB", sizeMB))
			}
		}

		// Compaction stats: use cache when available, show "Computing..." while pending.
		if sess.Metadata.TranscriptPath != "" {
			if qs, ok := statsCache[sess.Metadata.TranscriptPath]; ok {
				lines = append(lines, sep)
				lines = append(lines, "Context")
				if qs.EstimatedTokens > 0 {
					lines = append(lines, fmt.Sprintf("Tokens:      ~%s", formatTokenCount(int64(qs.EstimatedTokens))))
				}
				lines = append(lines, fmt.Sprintf("Compactions: %d", qs.Compactions))
				lines = append(lines, fmt.Sprintf("In context:  %s entries", formatCount(int64(qs.EntriesInContext))))
				if qs.Compactions > 0 && !qs.LastCompactTime.IsZero() {
					lines = append(lines, fmt.Sprintf("Last compact: %s", util.FormatRelativeTime(qs.LastCompactTime)))
				}
				lines = append(lines, fmt.Sprintf("Total:       %s entries", formatCount(int64(qs.TotalEntries))))
			} else {
				lines = append(lines, sep)
				lines = append(lines, "Context")
				lines = append(lines, "Computing...")
			}
		}

		// Last 5 non-tool messages
		if sess.Metadata.TranscriptPath != "" {
			messages := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 5, 150)
			// Filter out messages that start with '<' (system tags) or are very short
			var filtered []claude.RecentMessage
			for _, m := range messages {
				if strings.HasPrefix(m.Text, "<") {
					continue
				}
				if len(m.Text) < 5 {
					continue
				}
				filtered = append(filtered, m)
			}

			if len(filtered) > 0 {
				lines = append(lines, sep)
				lines = append(lines, "Last exchange:")
				for _, msg := range filtered {
					role := "You"
					if msg.Role == "assistant" {
						role = "Claude"
					}
					text := msg.Text
					if len(text) > 80 {
						text = text[:80] + "..."
					}
					lines = append(lines, fmt.Sprintf("  %s: %s", role, text))
				}
			}
		}

		// UUID + resume
		lines = append(lines, sep)
		lines = append(lines, fmt.Sprintf("UUID: %s", sess.Metadata.SessionID))
		lines = append(lines, fmt.Sprintf("clotilde resume %s", sess.Name))

		return strings.Join(lines, "\n")
	}
}

// formatTokenCount formats a token count as a human-readable string (e.g. "~13M", "~310k").
func formatTokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.0fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatCount formats an integer with comma separators (e.g. 30259 → "30,259").
func formatCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// shortWorkspacePath abbreviates a workspace root path for display.
// e.g. /Users/alex/Sites/configs → ~/Sites/configs, /Users/alex → ~
func shortWorkspacePath(root string) string {
	if root == "" {
		return "-"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Base(root)
	}
	if root == home {
		return "~"
	}
	if strings.HasPrefix(root, home+"/") {
		return "~/" + root[len(home)+1:]
	}
	return root
}
