package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/claude"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/daemon"
	"goodkind.io/clyde/internal/session"
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

// projectClydeRootForSession returns the project-level .claude/clyde path
// for a session. Used when computing transcript/agent-log paths (which are
// stored per-project in ~/.claude/projects/<encoded-project-path>/).
func projectClydeRootForSession(sess *session.Session) string {
	root := sess.Metadata.WorkspaceRoot
	if root == "" {
		root, _ = config.FindProjectRoot()
	}
	return filepath.Join(root, config.ClydeDir)
}

// allTranscriptPaths returns paths for all transcripts associated with a session,
// in chronological order: previous UUIDs first (oldest to newest), then the current one.
// The current path comes from metadata when available; otherwise it is computed from the UUID.
// Callers should skip paths that do not exist on disk (missing transcripts are not an error).
func allTranscriptPaths(sess *session.Session, clydeRoot, homeDir string) []string {
	var paths []string

	for _, prevID := range sess.Metadata.PreviousSessionIDs {
		if prevID == "" {
			continue
		}
		paths = append(paths, claude.TranscriptPath(homeDir, clydeRoot, prevID))
	}

	current := sess.Metadata.TranscriptPath
	if current == "" && sess.Metadata.SessionID != "" {
		current = claude.TranscriptPath(homeDir, clydeRoot, sess.Metadata.SessionID)
	}
	if current != "" {
		paths = append(paths, current)
	}

	return paths
}

// printResumeInstructions prints how to resume a session after claude exits.
// Skipped for incognito sessions (they auto-delete).
func printResumeInstructions(sess *session.Session) {
	if sess.Metadata.IsIncognito {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout)
	_, _ = fmt.Fprintln(os.Stdout, "Resume this session with:")
	_, _ = fmt.Fprintf(os.Stdout, "  clyde resume %s\n", sess.Name)
	_, _ = fmt.Fprintf(os.Stdout, "  claude --resume %s\n", sess.Metadata.SessionID)
	slog.Info("cmd.session.resume_instructions", "session", sess.Name, "session_id", sess.Metadata.SessionID)
}

// refreshSessionSummary requests a fresh Context summary via the daemon and
// polls the on-disk metadata until it changes, up to a short timeout. The
// onDone callback fires with the updated session once the new Context is
// persisted. It is called with nil if the refresh times out or fails.
//
// Unlike autoUpdateContext this function is called from the live TUI, not
// on session exit, so it has to watch for completion to redraw the row.
// The daemon runs the LLM call in the background; polling is necessary
// because the gRPC call returns before the LLM finishes.
func refreshSessionSummary(store session.Store, sess *session.Session, onDone func(*session.Session)) error {
	if sess == nil {
		return fmt.Errorf("nil session")
	}
	if sess.Metadata.IsIncognito || sess.Metadata.TranscriptPath == "" {
		return nil
	}

	// Pull a wide window of messages so the labeler can see the arc of
	// the conversation, not just the closing turns. A hundred messages
	// at 300 runes each fits comfortably in a single prompt and gives
	// the model enough signal to extract a topic.
	recent := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 100, 300)
	if len(recent) == 0 {
		return nil
	}

	messages := make([]string, 0, len(recent))
	for _, m := range recent {
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		messages = append(messages, fmt.Sprintf("[%s] %s", role, m.Text))
	}

	ctx := context.Background()
	client, err := daemon.ConnectOrStart(ctx)
	if err != nil {
		return err
	}
	// Dispatch the request. The daemon returns immediately; the LLM call
	// happens in a separate goroutine inside the daemon process.
	if err := client.UpdateContext(sess.Name, sess.Metadata.WorkspaceRoot, messages); err != nil {
		client.Close()
		return err
	}
	client.Close()

	// Poll in a goroutine so the UI stays responsive.
	originalCtx := sess.Metadata.Context
	go func() {
		deadline := time.Now().Add(45 * time.Second)
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		for time.Now().Before(deadline) {
			<-ticker.C
			updated, err := store.Get(sess.Name)
			if err != nil || updated == nil {
				continue
			}
			if updated.Metadata.Context != originalCtx {
				if onDone != nil {
					onDone(updated)
				}
				return
			}
		}
		if onDone != nil {
			onDone(nil)
		}
	}()
	return nil
}

// autoUpdateContext sends a fire-and-forget request to the daemon to generate
// a context summary for the session. Extracts recent messages here (avoiding
// import cycles in the daemon package), then sends them via gRPC. The daemon
// runs the LLM call in the background so the wrapper can exit immediately.
func autoUpdateContext(_ *session.FileStore, sess *session.Session) {
	if sess.Metadata.IsIncognito || sess.Metadata.TranscriptPath == "" {
		return
	}

	// Extract messages on the client side (claude package can't be imported by daemon).
	// Sample 100 messages so the labeler sees the conversation arc.
	recent := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 100, 300)
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
// with CLI-specific additions: auto-adopt for UUIDs and TUI picker for ambiguous matches.
// Returns nil session (no error) if nothing found. The caller should forward to claude.
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
	if err != nil || len(matches) <= 1 {
		return nil, nil
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Multiple sessions match '%s':\n", query)
	for _, s := range matches {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\n", s.Name, s.Metadata.SessionID)
	}
	return nil, fmt.Errorf("ambiguous session name '%s'; specify the full name", query)
}
