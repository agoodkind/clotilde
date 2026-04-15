package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/outputstyle"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/ui"
)

func newRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rename <old-name> <new-name>",
		Aliases: []string{"mv"},
		Short:   "Rename a session",
		Long: `Rename an existing session to a new name.

The session directory, metadata, and any custom output style are all updated.
Transcripts are not moved — they are stored by UUID, not by session name, so
they remain accessible after the rename.

Child sessions that were forked from this session retain the old parent name in
their 'parentSession' field (informational only; they are not affected).

It is safe to rename a session that is currently active. The running Claude
instance uses the session UUID, not the name, so it is unaffected. The new name
takes effect for all future 'clotilde resume' and 'clotilde list' operations.`,
		Args:               cobra.ExactArgs(2),
		ValidArgsFunction:  sessionNameCompletion,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			oldArg, newName := args[0], args[1]

			store, err := globalStore()
			if err != nil {
				return err
			}
			globalRoot := config.GlobalDataDir()

			// Resolve old-name: accept UUID or session name.
			oldName := oldArg
			if looksLikeUUID(oldArg) {
				resolved, resolveErr := findSessionByUUID(store, oldArg)
				if resolveErr != nil {
					return fmt.Errorf("no session found with UUID %s", oldArg)
				}
				oldName = resolved
			}

			if err := session.ValidateName(oldName); err != nil {
				return fmt.Errorf("invalid old name: %w", err)
			}
			if err := session.ValidateName(newName); err != nil {
				return fmt.Errorf("invalid new name: %w", err)
			}

			if !store.Exists(oldName) {
				return fmt.Errorf("session '%s' not found", oldName)
			}
			if store.Exists(newName) {
				return fmt.Errorf("session '%s' already exists", newName)
			}

			sess, err := store.Get(oldName)
			if err != nil {
				return fmt.Errorf("failed to load session: %w", err)
			}

			// Rename the session directory (atomic: both paths are siblings under sessions/).
			oldDir := config.GetSessionDir(globalRoot, oldName)
			newDir := config.GetSessionDir(globalRoot, newName)
			if err := os.Rename(oldDir, newDir); err != nil {
				return fmt.Errorf("rename session directory: %w", err)
			}

			// Update metadata — store.Update resolves via the new directory.
			sess.Name = newName
			sess.Metadata.Name = newName
			if err := store.Update(sess); err != nil {
				return fmt.Errorf("update session metadata: %w", err)
			}

			// Rename custom output style if present.
			outputStyleRoot := config.GlobalOutputStyleRoot()
			if sess.Metadata.HasCustomOutputStyle {
				if styleErr := outputstyle.RenameCustomStyleFile(outputStyleRoot, oldName, newName); styleErr != nil {
					// Style file may have been deleted manually — warn but do not fail.
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Warning(fmt.Sprintf("Failed to rename output style file: %v", styleErr)))
				} else {
					// Update the outputStyle reference in settings.json.
					settings, loadErr := store.LoadSettings(newName)
					if loadErr == nil && settings != nil && strings.HasPrefix(settings.OutputStyle, "clotilde/") {
						settings.OutputStyle = outputstyle.GetCustomStyleReference(newName)
						if saveErr := store.SaveSettings(newName, settings); saveErr != nil {
							_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Warning(fmt.Sprintf("Failed to update output style reference in settings: %v", saveErr)))
						}
					}
				}
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Success(fmt.Sprintf("Renamed session '%s' to '%s'", oldName, newName)))
			return nil
		},
	}
}
