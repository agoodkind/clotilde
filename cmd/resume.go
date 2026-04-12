package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/fgrehm/clotilde/internal/util"
)

// newResumeCmd creates a fresh resume command instance (avoids flag pollution in tests)
func newResumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume [name] [-- <claude-flags>...]",
		Short: "Resume an existing session by name",
		Long: `Resume a Claude Code session by its human-friendly name.

If no session name is provided, an interactive picker will be shown
(in TTY environments).

Pass additional flags to Claude Code after '--':
  clotilde resume my-session -- --debug api,hooks`,
		Args:               maxPositionalArgs(1),
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		ValidArgsFunction: sessionNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Use global session store
			store, err := globalStore()
			if err != nil {
				return err
			}
			clotildeRoot := config.GlobalDataDir()

			args, earlyAdditionalArgs := splitArgs(cmd, args)

			// Determine session name
			var name string
			if len(args) == 0 {
				// No session name — show workspace-scoped picker in TTY or list in non-interactive mode.
				workspaceRoot, _ := config.FindProjectRoot()

				loadSessions := func() ([]*session.Session, error) {
					if workspaceRoot != "" {
						return store.ListForWorkspace(workspaceRoot)
					}
					return store.List()
				}

				isTTY := isatty.IsTerminal(os.Stdout.Fd())
				if !isTTY {
					sessions, listErr := loadSessions()
					if listErr != nil {
						return fmt.Errorf("failed to list sessions: %w", listErr)
					}
					return showStaticTable(cmd, sessions, store)
				}

				// Load workspace-scoped sessions
				sessions, err := loadSessions()
				if err != nil {
					return fmt.Errorf("failed to list sessions: %w", err)
				}

				if len(sessions) == 0 {
					return fmt.Errorf("no sessions available (use 'clotilde list --all' to see all workspaces)")
				}

				// Sort by last accessed (most recent first)
				sortSessionsByLastAccessed(sessions)

				// Show picker with preview pane
				picker := ui.NewPicker(sessions, "Select session to resume").WithPreview()
				selected, err := ui.RunPicker(picker)
				if err != nil {
					return fmt.Errorf("picker failed: %w", err)
				}

				if selected == nil {
					// User cancelled
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}

				name = selected.Name
			} else {
				name = args[0]
				if looksLikeUUID(name) {
					resolved, resolveErr := findSessionByUUID(store, name)
					if resolveErr != nil {
						// Not in global store — try to find and adopt the transcript.
						adoptedName, adoptErr := tryAdoptByUUID(name)
						if adoptErr != nil {
							return fmt.Errorf("no session found with UUID %s", name)
						}
						_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Auto-adopted session '%s'\n", adoptedName)
						name = adoptedName
					} else {
						name = resolved
					}
				}
			}

			// Merge flags that were collected before name parsing with any remaining
			// additional args (nothing left after the dash split above).
			additionalArgs := earlyAdditionalArgs

			// Resolve shorthand flags (resume doesn't create sessions, pass to claude CLI)
			permMode, err := resolvePermissionMode(cmd)
			if err != nil {
				return err
			}
			if permMode != "" {
				additionalArgs = append(additionalArgs, "--permission-mode", permMode)
			}

			fastEnabled, err := resolveFastMode(cmd)
			if err != nil {
				return err
			}
			if fastEnabled {
				additionalArgs = append(additionalArgs, "--model", "haiku", "--effort", "low")
			} else {
				if model, _ := cmd.Flags().GetString("model"); model != "" {
					additionalArgs = append(additionalArgs, "--model", normalizeModel(model))
				}
				additionalArgs = collectEffortFlag(cmd, additionalArgs)
			}

			// Load session by name, falling back to display name lookup.
			// If still not found, let Claude resolve the name directly
			// (handles sessions started outside clotilde). Daemon wrapping
			// in invokeInteractive still provides model isolation.
			sess, err := store.Get(name)
			if err != nil {
				sess, err = store.GetByDisplayName(name)
				if err != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Session '%s' not in clotilde, resuming via Claude...\n\n", name)
					return claude.ResumeByName(name, additionalArgs)
				}
				name = sess.Name
			}

			// Update context if --context flag provided
			contextFlag, _ := cmd.Flags().GetString("context")
			if contextFlag != "" {
				sess.Metadata.Context = contextFlag
			}

			// Update lastAccessed timestamp
			sess.UpdateLastAccessed()
			if err := store.Update(sess); err != nil {
				return fmt.Errorf("failed to update session: %w", err)
			}

			sessionDir := config.GetSessionDir(clotildeRoot, name)

			// Check for settings file
			var settingsFile string
			settingsPath := filepath.Join(sessionDir, "settings.json")
			if util.FileExists(settingsPath) {
				settingsFile = settingsPath
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Resuming session '%s' (%s)\n\n", name, sess.Metadata.SessionID)

			// Invoke claude
			return claude.Resume(clotildeRoot, sess, settingsFile, additionalArgs)
		},
	}
	cmd.Flags().String("model", "", "Claude model to use (haiku, sonnet, opus); opus defaults to 1M context")
	cmd.Flags().String("context", "", "Session context (e.g. \"working on ticket GH-123\")")
	registerShorthandFlags(cmd)
	_ = cmd.RegisterFlagCompletionFunc("model", modelCompletion)
	return cmd
}

// sortSessionsByLastAccessed sorts sessions by last accessed time (most recent first)
func sortSessionsByLastAccessed(sessions []*session.Session) {
	// Simple bubble sort - good enough for typical session counts
	for i := range len(sessions) - 1 {
		for j := range len(sessions) - i - 1 {
			if sessions[j].Metadata.LastAccessed.Before(sessions[j+1].Metadata.LastAccessed) {
				sessions[j], sessions[j+1] = sessions[j+1], sessions[j]
			}
		}
	}
}
