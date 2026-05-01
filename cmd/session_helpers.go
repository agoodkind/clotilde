package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/daemon"
	"goodkind.io/clyde/internal/session"
	sessionlifecycle "goodkind.io/clyde/internal/session/lifecycle"
)

// globalStore returns the global session store, or panics on error.
// Used by commands that always need the global store and treat an error as fatal.
func globalStore() (*session.FileStore, error) {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, fmt.Errorf("failed to open session store: %w", err)
	}
	return store, nil
}

// printResumeInstructions prints how to resume a session after the interactive
// provider exits.
// Skipped for incognito sessions (they auto-delete).
func printResumeInstructions(sess *session.Session) {
	if sess.Metadata.IsIncognito {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout)
	_, _ = fmt.Fprintln(os.Stdout, "Resume this session with:")
	_, _ = fmt.Fprintf(os.Stdout, "  clyde resume %s\n", sess.Name)
	runtime, err := sessionlifecycle.ForSession(sess, nil)
	if err != nil {
		slog.Warn("cmd.session.resume_instructions_provider_failed",
			"component", "cli",
			"session", sess.Name,
			"provider", sess.ProviderID(),
			"err", err,
		)
	} else {
		for _, line := range runtime.ResumeInstructions(sess) {
			_, _ = fmt.Fprintf(os.Stdout, "  %s\n", line)
		}
	}
	slog.Info("cmd.session.resume_instructions", "session", sess.Name, "session_id", sess.Metadata.SessionID)
}

// autoUpdateContext sends a fire-and-forget request to the daemon to generate
// a context summary for the session. Extracts recent messages here (avoiding
// import cycles in the daemon package), then sends them via gRPC. The daemon
// runs the LLM call in the background so the wrapper can exit immediately.
func autoUpdateContext(_ *session.FileStore, sess *session.Session) {
	if sess.Metadata.IsIncognito {
		return
	}

	runtime, err := sessionlifecycle.ForSession(sess, nil)
	if err != nil {
		return
	}

	// Sample 100 messages so the labeler sees the conversation arc.
	recent := runtime.RecentContextMessages(sess, 100, 300)
	if len(recent) == 0 {
		return
	}
	var messages []string
	for _, msg := range recent {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		messages = append(messages, fmt.Sprintf("[%s] %s", role, msg.Text))
	}

	ctx := context.Background()
	client, err := daemon.ConnectOrStart(ctx)
	if err != nil {
		return
	}
	defer client.Close()

	_ = client.UpdateContext(sess.Name, sess.Metadata.WorkspaceRoot, messages)
}

// resolveSessionForResume finds a session using the store's unified resolution,
// with CLI-specific additions: auto-adopt for UUIDs and TUI picker for
// ambiguous matches. Returns nil session (no error) if nothing found; the
// caller can then fall back to the default provider runtime.
func resolveSessionForResume(cmd *cobra.Command, store *session.FileStore, query string) (*session.Session, error) {
	// Unified 4-tier resolution: exact name, UUID, display name, single
	// substring match. Anything more ambiguous than a single match is
	// listed and rejected so the user picks unambiguously themselves.
	// The TUI dashboard exists for interactive multi-match selection;
	// this CLI verb stays scriptable.
	if sess, err := store.Resolve(query); err != nil {
		return nil, err
	} else if sess != nil {
		return sess, nil
	}
	matches, err := store.Search(query)
	if err != nil {
		return nil, err
	}
	if len(matches) <= 1 {
		return nil, nil
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Multiple sessions match '%s':\n", query)
	for _, s := range matches {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\n", s.Name, s.Metadata.SessionID)
	}
	return nil, fmt.Errorf("ambiguous session name '%s'; specify the full name", query)
}
