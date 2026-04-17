package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newSetBasedirCmd ships the `clotilde set-basedir <session> <path>`
// command. It updates Metadata.WorkspaceRoot (shown as "Basedir" in the
// TUI) so the user can correct a misattributed session or move a session
// after restructuring a project tree. The transcript and Claude Code
// UUID are left untouched; only the clotilde metadata file changes.
func newSetBasedirCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-basedir <session> <path>",
		Short: "Change a session's basedir (workspace root) shown in the TUI",
		Long: `Change the basedir for a named session. This updates the
workspaceRoot field in the session metadata. The transcript, Claude Code
UUID, and session contents are not touched.

Paths:
  - Absolute paths are used as-is.
  - Relative paths are resolved against the current working directory.
  - The literal string "." means the current working directory.
  - The special value "-" clears the basedir.

The target path is not required to exist. The TUI will still display the
string, and nothing else reads the field at runtime.

Examples:
  clotilde set-basedir my-session ~/Sites/clotilde
  clotilde set-basedir my-session .
  clotilde set-basedir stale-session -`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			target := args[1]

			store, err := globalStore()
			if err != nil {
				return err
			}
			sess, err := store.Get(name)
			if err != nil || sess == nil {
				return fmt.Errorf("session '%s' not found", name)
			}

			resolved, err := resolveBasedirArg(target)
			if err != nil {
				return err
			}

			old := sess.Metadata.WorkspaceRoot
			sess.Metadata.WorkspaceRoot = resolved
			if err := store.Update(sess); err != nil {
				return fmt.Errorf("update metadata: %w", err)
			}

			out := cmd.OutOrStdout()
			if resolved == "" {
				_, _ = fmt.Fprintf(out, "Cleared basedir for %q (was %q)\n", name, old)
				return nil
			}
			_, _ = fmt.Fprintf(out, "Set basedir for %q: %s\n", name, resolved)
			if old != "" && old != resolved {
				_, _ = fmt.Fprintf(out, "  (previously: %s)\n", old)
			}
			return nil
		},
	}
	return cmd
}

// resolveBasedirArg maps a user-supplied basedir string to the value that
// should land in metadata. An empty string or "-" clears the field. A "."
// maps to the CWD. Relative paths become absolute. Tilde is expanded.
func resolveBasedirArg(raw string) (string, error) {
	if raw == "" || raw == "-" {
		return "", nil
	}
	// Tilde expansion for the common case.
	if len(raw) >= 2 && raw[0] == '~' && (raw[1] == '/' || raw[1] == os.PathSeparator) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		raw = filepath.Join(home, raw[2:])
	}
	if raw == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if !filepath.IsAbs(raw) {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", raw, err)
		}
		raw = abs
	}
	return filepath.Clean(raw), nil
}
