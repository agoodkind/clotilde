package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/fgrehm/clotilde/internal/util"
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

// autoUpdateContext generates a one-sentence context summary for a session
// using sonnet, reading recent messages and project memories. Non-fatal: silently
// skips on any error. Called after claude exits.
func autoUpdateContext(store *session.FileStore, sess *session.Session) {
	if sess.Metadata.IsIncognito {
		return
	}
	if sess.Metadata.TranscriptPath == "" {
		return
	}

	// Read last 5 user messages from transcript
	messages := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 5, 300)
	if len(messages) == 0 {
		return
	}

	// Build prompt with recent messages
	var promptParts []string
	promptParts = append(promptParts, "Based on these recent messages from a coding session, write ONE short sentence (under 15 words) describing what this session is currently working on. Be specific — mention the project, feature, or task name. Output ONLY the sentence, nothing else.")
	promptParts = append(promptParts, "")

	// Include project memory index if available
	memoryPath := projectMemoryPath(sess)
	if memoryPath != "" {
		memoryContent, err := os.ReadFile(memoryPath)
		if err == nil && len(memoryContent) > 0 {
			// Truncate to avoid blowing up the prompt
			mem := string(memoryContent)
			if len(mem) > 2000 {
				mem = mem[:2000]
			}
			promptParts = append(promptParts, "Project memory index:")
			promptParts = append(promptParts, mem)
			promptParts = append(promptParts, "")
		}
	}

	promptParts = append(promptParts, "Recent messages:")
	for _, msg := range messages {
		role := "User"
		if msg.Role == "assistant" {
			role = "Assistant"
		}
		promptParts = append(promptParts, fmt.Sprintf("[%s] %s", role, msg.Text))
	}

	prompt := strings.Join(promptParts, "\n")

	// Call claude -p --model sonnet with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	claudeBin := claude.ClaudeBinaryPathFunc()
	cmd := exec.CommandContext(ctx, claudeBin, "-p", "--model", "sonnet", prompt)
	output, err := cmd.Output()
	if err != nil {
		return // non-fatal
	}

	summary := strings.TrimSpace(string(output))
	if summary == "" || len(summary) > 200 {
		return
	}

	// Reload session to avoid overwriting concurrent changes
	current, err := store.Get(sess.Name)
	if err != nil {
		return
	}
	current.Metadata.Context = summary
	_ = store.Update(current)
}

// projectMemoryPath returns the path to MEMORY.md for a session's workspace.
// Returns "" if not found.
func projectMemoryPath(sess *session.Session) string {
	root := sess.Metadata.WorkspaceRoot
	if root == "" {
		return ""
	}
	home, err := util.HomeDir()
	if err != nil {
		return ""
	}
	projClotildeRoot := filepath.Join(root, ".claude", "clotilde")
	encodedDir := claude.ProjectDir(projClotildeRoot)
	memPath := filepath.Join(home, ".claude", "projects", encodedDir, "memory", "MEMORY.md")
	if util.FileExists(memPath) {
		return memPath
	}
	return ""
}

// resolveSessionForResume finds a session by trying multiple lookup strategies:
// 1. Exact name match
// 2. UUID match (with auto-adopt)
// 3. Display name match
// 4. Substring search → if multiple results, show TUI picker
// Returns nil session (no error) if nothing found — caller should forward to claude.
func resolveSessionForResume(cmd *cobra.Command, store *session.FileStore, query string) (*session.Session, error) {
	// 1. Exact name match
	if sess, err := store.Get(query); err == nil {
		return sess, nil
	}

	// 2. UUID match
	if looksLikeUUID(query) {
		resolved, err := findSessionByUUID(store, query)
		if err == nil {
			sess, _ := store.Get(resolved)
			return sess, nil
		}
		// Try auto-adopt
		adoptedName, adoptErr := tryAdoptByUUID(query)
		if adoptErr == nil {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Auto-adopted session '%s'\n", adoptedName)
			sess, _ := store.Get(adoptedName)
			return sess, nil
		}
	}

	// 3. Display name match
	if sess, err := store.GetByDisplayName(query); err == nil {
		return sess, nil
	}

	// 4. Substring search
	matches, err := store.Search(query)
	if err != nil || len(matches) == 0 {
		return nil, nil // not found — caller forwards to claude
	}

	if len(matches) == 1 {
		return matches[0], nil
	}

	// Multiple matches — show TUI picker if interactive
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Multiple sessions match '%s':\n", query)
		for _, s := range matches {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s)\n", s.Name, s.Metadata.SessionID)
		}
		return nil, fmt.Errorf("ambiguous session name '%s' — specify the full name", query)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Multiple sessions match '%s':\n\n", query)
	sortSessionsByLastAccessed(matches)
	picker := ui.NewPicker(matches, "Select session to resume").WithPreview()
	picker.PreviewFn = richPreviewFunc(store)
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
