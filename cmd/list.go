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
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/fgrehm/clotilde/internal/util"
)

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List sessions",
		Long: `List clotilde sessions, filtered to the current workspace by default.

Use --all to show every session across all workspaces.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			showAll, _ := cmd.Flags().GetBool("all")

			store, err := globalStore()
			if err != nil {
				return err
			}

			var sessions []*session.Session
			if showAll {
				sessions, err = store.List()
			} else {
				workspaceRoot, wsErr := config.FindProjectRoot()
				if wsErr != nil {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nCreate a session with:")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  clotilde start <session-name>")
					return nil
				}
				sessions, err = store.ListForWorkspace(workspaceRoot)
			}
			if err != nil {
				return fmt.Errorf("failed to list sessions: %w", err)
			}

			if len(sessions) == 0 {
				if showAll {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
				} else {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No sessions for this workspace.")
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Use --all to see every session, or:")
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nCreate a session with:")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  clotilde start <session-name>")
				return nil
			}

			return showStaticTable(cmd, sessions, store)
		},
	}
	cmd.Flags().Bool("all", false, "Show sessions from all workspaces")
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
// including UUID, workspace, context, model, transcript size, and recent messages.
func richPreviewFunc(store session.Store) ui.PreviewFunc {
	return func(sess *session.Session) string {
		var lines []string

		// Name
		lines = append(lines, sess.Name)
		lines = append(lines, "")

		// UUID
		lines = append(lines, fmt.Sprintf("UUID:        %s", sess.Metadata.SessionID))

		// Display name (if different)
		if sess.Metadata.DisplayName != "" && sess.Metadata.DisplayName != sess.Name {
			lines = append(lines, fmt.Sprintf("Display:     %s", sess.Metadata.DisplayName))
		}

		// Workspace + WorkDir
		lines = append(lines, fmt.Sprintf("Workspace:   %s", shortWorkspacePath(sess.Metadata.WorkspaceRoot)))
		if sess.Metadata.WorkDir != "" && sess.Metadata.WorkDir != sess.Metadata.WorkspaceRoot {
			lines = append(lines, fmt.Sprintf("Work dir:    %s", shortWorkspacePath(sess.Metadata.WorkDir)))
		}

		// Model
		model, _ := extractModelAndLastUsed(sess, store)
		lines = append(lines, fmt.Sprintf("Model:       %s", model))

		// Context
		if sess.Metadata.Context != "" {
			lines = append(lines, fmt.Sprintf("Context:     %s", sess.Metadata.Context))
		}

		// Type
		if sess.Metadata.IsForkedSession {
			lines = append(lines, fmt.Sprintf("Type:        fork of %s", sess.Metadata.ParentSession))
		}
		if sess.Metadata.IsIncognito {
			lines = append(lines, "Type:        incognito")
		}

		lines = append(lines, "")

		// Timestamps
		lines = append(lines, fmt.Sprintf("Created:     %s", sess.Metadata.Created.Format("2006-01-02 15:04")))
		lines = append(lines, fmt.Sprintf("Last used:   %s", util.FormatRelativeTime(sess.Metadata.LastAccessed)))

		// Transcript size
		if sess.Metadata.TranscriptPath != "" {
			if info, err := os.Stat(sess.Metadata.TranscriptPath); err == nil {
				size := float64(info.Size()) / (1024 * 1024)
				lines = append(lines, fmt.Sprintf("Transcript:  %.1f MB", size))
			}
		}

		// Recent messages
		if sess.Metadata.TranscriptPath != "" {
			messages := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 3, 120)
			if len(messages) > 0 {
				lines = append(lines, "")
				lines = append(lines, "Recent:")
				for _, msg := range messages {
					prefix := "  > "
					if msg.Role == "assistant" {
						prefix = "  < "
					}
					lines = append(lines, prefix+msg.Text)
				}
			}
		}

		// Resume command
		lines = append(lines, "")
		lines = append(lines, "Resume with:")
		lines = append(lines, fmt.Sprintf("  clotilde resume %s", sess.Name))

		return strings.Join(lines, "\n")
	}
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
