package cmd

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/claude"
)

// NewResumeCmd implements `clyde resume <name|uuid>`. It resolves the
// argument against the clyde session store (by name, UUID, display
// name, or fuzzy match) and shells out to `claude --resume <real-uuid>`.
// When nothing matches, it forwards the raw argument to
// claude.ResumeByName so Claude-native sessions resume transparently.
//
// `clyde -r <uuid>` and `clyde --resume <uuid>` are rewritten to this
// verb by ClassifyArgs in dispatch.go, so all three forms share one
// RunE.
func NewResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "resume <name|uuid>",
		Short:              "Resolve a clyde session name to its UUID and resume it via claude",
		Args:               cobra.ExactArgs(1),
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			slog.Info("cli.resume.invoked",
				"component", "cli",
				"query", query,
			)
			store, err := globalStore()
			if err != nil {
				return err
			}
			sess, err := resolveSessionForResume(cmd, store, query)
			if err != nil {
				return err
			}
			if sess == nil {
				slog.Info("cli.resume.unknown_session.forwarding_to_claude",
					"component", "cli",
					"query", query,
				)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(),
					"Session '%s' not in clyde; forwarding to claude.\n\n", query)
				return claude.ResumeByName(query, nil)
			}
			slog.Info("cli.resume.resolved",
				"component", "cli",
				"query", query,
				"session", sess.Name,
				"session_id", sess.Metadata.SessionID,
			)
			return resumeSession(sess, store)
		},
	}
}
