package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/session"
	sessionlifecycle "goodkind.io/clyde/internal/session/lifecycle"
)

// NewResumeCmd implements `clyde resume <name|uuid>`. It resolves the
// argument against the clyde session store (by name, UUID, display
// name, or fuzzy match) and shells out through the provider runtime with the
// resolved provider session id. When nothing matches, it forwards the raw
// query to the default provider runtime so upstream-native sessions resume
// transparently.
//
// `clyde -r <uuid>` and `clyde --resume <uuid>` are rewritten to this
// verb by ClassifyArgs in dispatch.go, so all three forms share one
// RunE.
func NewResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "resume <name|uuid>",
		Short:              "Resolve a clyde session name to its provider session id and resume it",
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
				slog.Info("cli.resume.unknown_session.forwarding_to_provider",
					"component", "cli",
					"query", query,
				)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(),
					"Session '%s' not in clyde; forwarding to the default provider.\n\n", query)
				runtime, err := sessionlifecycle.Default(store)
				if err != nil {
					return err
				}
				return runtime.ResumeOpaqueInteractive(context.Background(), session.OpaqueResumeRequest{
					Query: query,
				})
			}
			slog.Info("cli.resume.resolved",
				"component", "cli",
				"query", query,
				"session", sess.Name,
				"session_id", sess.Metadata.SessionID,
			)
			return resumeSession(sess, store, false)
		},
	}
}
