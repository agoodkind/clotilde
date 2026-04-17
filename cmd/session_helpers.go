package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/daemon"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/google/uuid"
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

// projectClotildeRootForSession returns the project-level .claude/clotilde path
// for a session. Used when computing transcript/agent-log paths (which are
// stored per-project in ~/.claude/projects/<encoded-project-path>/).
func projectClotildeRootForSession(sess *session.Session) string {
	root := sess.Metadata.WorkspaceRoot
	if root == "" {
		root, _ = config.FindProjectRoot()
	}
	return filepath.Join(root, config.ClotildeDir)
}

// looksLikeUUID returns true if s is a valid UUID.
func looksLikeUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// allTranscriptPaths returns paths for all transcripts associated with a session,
// in chronological order: previous UUIDs first (oldest to newest), then the current one.
// The current path comes from metadata when available; otherwise it is computed from the UUID.
// Callers should skip paths that do not exist on disk (missing transcripts are not an error).
func allTranscriptPaths(sess *session.Session, clotildeRoot, homeDir string) []string {
	var paths []string

	for _, prevID := range sess.Metadata.PreviousSessionIDs {
		if prevID == "" {
			continue
		}
		paths = append(paths, claude.TranscriptPath(homeDir, clotildeRoot, prevID))
	}

	current := sess.Metadata.TranscriptPath
	if current == "" && sess.Metadata.SessionID != "" {
		current = claude.TranscriptPath(homeDir, clotildeRoot, sess.Metadata.SessionID)
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
	fmt.Println()
	fmt.Println("Resume this session with:")
	fmt.Printf("  clotilde resume %s\n", sess.Name)
	fmt.Printf("  claude --resume %s\n", sess.Metadata.SessionID)
}

// returnToDashboard shows the dashboard TUI after a session exits,
// with "Return to <session>" at the top. Skipped in non-TTY environments.
func returnToDashboard(sess *session.Session) {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return
	}
	if sess == nil || sess.Metadata.IsIncognito {
		return
	}
	fmt.Println()
	runPostSessionDashboard(sess)
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

	recent := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 5, 300)
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

	// Extract messages on the client side (claude package can't be imported by daemon)
	recent := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 5, 300)
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
	// Try unified 4-tier resolution (name, UUID, display name, single substring match)
	if sess, err := store.Resolve(query); err != nil {
		return nil, err
	} else if sess != nil {
		return sess, nil
	}

	// CLI-specific: try auto-adopt for UUID queries
	if looksLikeUUID(query) {
		adoptedName, adoptErr := tryAdoptByUUID(query)
		if adoptErr == nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Auto-adopted session '%s'\n", adoptedName)
			sess, _ := store.Get(adoptedName)
			return sess, nil
		}
	}

	// CLI-specific: if multiple substring matches, show picker
	matches, err := store.Search(query)
	if err != nil || len(matches) <= 1 {
		return nil, nil // not found or single match already handled by Resolve
	}

	if !isatty.IsTerminal(os.Stdout.Fd()) {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Multiple sessions match '%s':\n", query)
		for _, s := range matches {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\n", s.Name, s.Metadata.SessionID)
		}
		return nil, fmt.Errorf("ambiguous session name '%s'; specify the full name", query)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Multiple sessions match '%s':\n\n", query)
	sortSessionsByLastAccessed(matches)
	picker := ui.NewPicker(matches, "Select session to resume").WithPreview()
	picker.PreviewFn = richPreviewFunc(store, picker.StatsCache)
	selected, pickerErr := ui.RunPicker(picker)
	if pickerErr != nil {
		return nil, fmt.Errorf("picker failed: %w", pickerErr)
	}
	if selected == nil {
		return nil, fmt.Errorf("cancelled")
	}
	return selected, nil
}

// resolveSessionName resolves the session name using a multi-level fallback strategy.
// Priority 1: CLOTILDE_SESSION_NAME env var (always checked).
// When fullFallback is true, also tries:
// Priority 2: Read from CLAUDE_ENV_FILE (persisted by previous hook).
// Priority 3: Reverse UUID lookup in session store.
func resolveSessionName(hookData hookInput, store session.Store, fullFallback bool) (string, error) {
	if name := os.Getenv("CLOTILDE_SESSION_NAME"); name != "" {
		return name, nil
	}

	if !fullFallback {
		return "", nil
	}

	if name := readLastEnvFileValue("CLOTILDE_SESSION"); name != "" {
		return name, nil
	}

	return findSessionByUUID(store, hookData.SessionID)
}

// findSessionByUUID searches for a session with the given UUID.
// Checks both current sessionId and previousSessionIds.
func findSessionByUUID(store session.Store, uuid string) (string, error) {
	sessions, err := store.List()
	if err != nil {
		return "", fmt.Errorf("failed to list sessions: %w", err)
	}

	for _, sess := range sessions {
		if sess.Metadata.SessionID == uuid {
			return sess.Name, nil
		}
	}

	for _, sess := range sessions {
		if slices.Contains(sess.Metadata.PreviousSessionIDs, uuid) {
			return sess.Name, nil
		}
	}

	return "", fmt.Errorf("no session found with UUID %s", uuid)
}
