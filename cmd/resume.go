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
		ValidArgsFunction:  sessionNameCompletion,
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
				// No session name: show all sessions in picker (TTY) or static table (pipe).
				isTTY := isatty.IsTerminal(os.Stdout.Fd())
				if !isTTY {
					sessions, listErr := store.List()
					if listErr != nil {
						return fmt.Errorf("failed to list sessions: %w", listErr)
					}
					return showStaticTable(cmd, sessions, store)
				}

				sessions, err := store.List()
				if err != nil {
					return fmt.Errorf("failed to list sessions: %w", err)
				}

				if len(sessions) == 0 {
					return fmt.Errorf("no sessions available")
				}

				// Sort by last accessed (most recent first)
				sortSessionsByLastAccessed(sessions)

				// Show picker with rich preview pane
				picker := ui.NewPicker(sessions, "Select session to resume").WithPreview()
				picker.PreviewFn = richPreviewFunc(store, picker.StatsCache)
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

				// Resolve session: exact name → UUID → display name → fuzzy search → forward to claude
				sess, resolveErr := resolveSessionForResume(cmd, store, name)
				if resolveErr != nil {
					return resolveErr
				}
				if sess == nil {
					// No match in clotilde — forward to claude directly
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Session '%s' not in clotilde, resuming via Claude...\n\n", name)
					return claude.ResumeByName(name, nil)
				}
				name = sess.Name
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

			// Load session
			sess, err := store.Get(name)
			if err != nil {
				return fmt.Errorf("session '%s' not found", name)
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

			// Auto add-dir if resuming from a different directory
			if cwd, cwdErr := os.Getwd(); cwdErr == nil {
				if sess.Metadata.WorkspaceRoot != "" && cwd != sess.Metadata.WorkspaceRoot {
					additionalArgs = append(additionalArgs, "--add-dir", cwd)
				}
			}

			if rcErr := applyRemoteControlFlag(cmd, sess); rcErr != nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Warning(fmt.Sprintf("remote control setting failed: %v", rcErr)))
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Resuming session '%s' (%s)\n\n", name, sess.Metadata.SessionID)

			// Invoke claude
			err = claude.Resume(clotildeRoot, sess, settingsFile, additionalArgs)
			autoUpdateContext(store, sess)
			printResumeInstructions(sess)
			returnToDashboard(sess)
			return err
		},
	}
	cmd.Flags().String("model", "", "Claude model to use (haiku, sonnet, opus); opus defaults to 1M context")
	cmd.Flags().String("context", "", "Session context (e.g. \"working on ticket GH-123\")")
	cmd.Flags().Bool("remote-control", false, "Launch with --remote-control so the session is exposed via claude.ai/code/<bridge>")
	cmd.Flags().Bool("no-remote-control", false, "Force-disable remote control even if the session previously had it on")
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
