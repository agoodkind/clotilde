package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/notify"
	"github.com/fgrehm/clotilde/internal/session"
)

// hookInput represents the JSON structure passed to SessionStart hooks.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Source         string `json:"source"` // startup, resume, compact, clear
}

var sessionStartCmd = &cobra.Command{
	Use:   "sessionstart",
	Short: "Unified SessionStart hook handler",
	Long: `Called by Claude Code's SessionStart hook for all sources (startup, resume, compact, clear).
Handles fork registration, session ID updates, and context injection.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Read hook input from stdin (must happen before guard check,
		// since we need session_id and source to scope the guard)
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read hook input: %w", err)
		}

		var hookData hookInput
		if err := json.Unmarshal(input, &hookData); err != nil {
			return fmt.Errorf("failed to parse hook input: %w", err)
		}

		// Log raw event for debugging (before any other processing)
		if err := notify.LogEvent(input, hookData.SessionID); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to log event: %v\n", err)
		}

		// Guard against double execution (global + per-project hooks).
		// Scoped to session_id:source so that different events (e.g. startup
		// vs clear) are not blocked by a previous invocation's marker.
		marker := hookData.SessionID + ":" + hookData.Source
		if isHookExecuted(marker) {
			return nil
		}

		// Use global session store — hooks run regardless of working directory.
		store, err := session.NewGlobalFileStore()
		if err != nil {
			// Can't open store; silently exit so we don't break Claude.
			return nil
		}

		// Mark as executed to prevent double-run from global + project hooks
		markHookExecuted(marker)

		// Dispatch based on source field
		switch hookData.Source {
		case "startup", "resume":
			return handleStartupOrResume(hookData, store)
		case "compact":
			return handleCompact(hookData, store)
		case "clear":
			return handleClear(hookData, store)
		default:
			// Fallback to startup for backward compatibility or unknown sources
			return handleStartupOrResume(hookData, store)
		}
	},
}

// handleStartupOrResume handles new session startup and session resumption.
// If CLOTILDE_SESSION_NAME is set but the session doesn't exist in the store,
// it auto-creates it (handles sessions started outside clotilde that are resumed
// through clotilde's drop-in dispatch).
func handleStartupOrResume(hookData hookInput, store session.Store) error {
	sessionName := os.Getenv("CLOTILDE_SESSION_NAME")

	if sessionName != "" {
		// Auto-create session if it doesn't exist yet (drop-in resume path)
		if !store.Exists(sessionName) && hookData.SessionID != "" {
			if session.ValidateName(sessionName) == nil {
				autoAdoptSession(store, sessionName, hookData)
			}
		}

		if err := writeSessionNameToEnv(sessionName); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to write session name to env: %v\n", err)
		}

		if hookData.TranscriptPath != "" {
			if err := saveTranscriptPath(store, sessionName, hookData.TranscriptPath); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to save transcript path: %v\n", err)
			}
		}
	}

	outputContexts(store, sessionName)

	return nil
}

// autoAdoptSession creates a new session in the store from hook data.
// Used when a session started outside clotilde is resumed through clotilde's
// drop-in dispatch — the hook fires and we capture the session into the store.
func autoAdoptSession(store session.Store, name string, hookData hookInput) {
	sess := session.NewSession(name, hookData.SessionID)
	sess.Metadata.TranscriptPath = hookData.TranscriptPath
	sess.Metadata.DisplayName = name

	if wd, err := os.Getwd(); err == nil {
		sess.Metadata.WorkDir = wd
	}
	if root, err := config.FindProjectRoot(); err == nil {
		sess.Metadata.WorkspaceRoot = root
	}

	if err := store.Create(sess); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: auto-adopt failed for '%s': %v\n", name, err)
	}
}

// handleCompact handles session compaction, updating session ID and preserving history.
// NOTE: Currently Claude Code does NOT create a new UUID for /compact (only /clear does).
// This handler is defensive programming in case Claude Code's behavior changes in the future.
func handleCompact(hookData hookInput, store session.Store) error {
	// Resolve session name using three-level fallback
	sessionName, err := resolveSessionName(hookData, store, true)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: unable to resolve session name for compact: %v\n", err)
		return nil
	}

	if sessionName == "" {
		return nil
	}

	// Load existing session
	sess, err := store.Get(sessionName)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: session '%s' not found: %v\n", sessionName, err)
		return nil
	}

	// Update session ID, preserving old ID in history
	sess.AddPreviousSessionID(hookData.SessionID)
	sess.Metadata.TranscriptPath = hookData.TranscriptPath
	sess.UpdateLastAccessed()

	if err := store.Update(sess); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to update session metadata: %v\n", err)
	}

	// Persist session name for next operation
	if err := writeSessionNameToEnv(sessionName); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Warning: failed to write session name to env: %v\n", err)
	}

	outputContexts(store, sessionName)

	return nil
}

// handleClear handles session clear - identical to compact.
// Unlike /compact, /clear DOES create a new session UUID in Claude Code.
func handleClear(hookData hookInput, store session.Store) error {
	return handleCompact(hookData, store)
}

// saveTranscriptPath saves the transcript path and updates lastAccessed in a single write.
func saveTranscriptPath(store session.Store, sessionName, transcriptPath string) error {
	sess, err := store.Get(sessionName)
	if err != nil {
		return fmt.Errorf("session '%s' not found: %w", sessionName, err)
	}

	sess.Metadata.TranscriptPath = transcriptPath
	sess.UpdateLastAccessed()

	if err := store.Update(sess); err != nil {
		return fmt.Errorf("failed to update session metadata: %w", err)
	}

	return nil
}

// isHookExecuted checks if a hook with this marker has already run.
// It checks both the env var (set by Claude Code after sourcing CLAUDE_ENV_FILE)
// and the file contents directly (in case Claude Code hasn't re-sourced yet).
// The marker is scoped to "session_id:source" so different events don't block each other.
func isHookExecuted(marker string) bool {
	if os.Getenv("CLOTILDE_HOOK_EXECUTED") == marker {
		return true
	}
	return readLastEnvFileValue("CLOTILDE_HOOK_EXECUTED") == marker
}

// markHookExecuted writes CLOTILDE_HOOK_EXECUTED=<marker> to CLAUDE_ENV_FILE
// so that a second hook invocation (from global + project hooks) for the same
// event is skipped.
func markHookExecuted(marker string) {
	_ = appendToEnvFile("CLOTILDE_HOOK_EXECUTED", marker)
}

// writeSessionNameToEnv writes the session name to Claude's env file for statusline use.
func writeSessionNameToEnv(sessionName string) error {
	return appendToEnvFile("CLOTILDE_SESSION", sessionName)
}

// readLastEnvFileValue reads CLAUDE_ENV_FILE and returns the last value
// assigned to the given key (KEY=value lines). Returns "" if not found.
// Uses last-wins semantics to match shell sourcing behavior.
func readLastEnvFileValue(key string) string {
	claudeEnvFile := os.Getenv("CLAUDE_ENV_FILE")
	if claudeEnvFile == "" {
		return ""
	}

	content, err := os.ReadFile(claudeEnvFile)
	if err != nil {
		return ""
	}

	prefix := key + "="
	var lastValue string
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, prefix); ok {
			lastValue = after
		}
	}
	return lastValue
}

// appendToEnvFile appends a KEY=value line to CLAUDE_ENV_FILE.
// Returns nil if CLAUDE_ENV_FILE is not set.
func appendToEnvFile(key, value string) error {
	claudeEnvFile := os.Getenv("CLAUDE_ENV_FILE")
	if claudeEnvFile == "" {
		return nil
	}

	f, err := os.OpenFile(claudeEnvFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open CLAUDE_ENV_FILE: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := fmt.Fprintf(f, "%s=%s\n", key, value); err != nil {
		return fmt.Errorf("failed to write to CLAUDE_ENV_FILE: %w", err)
	}
	return nil
}

// outputContexts loads and outputs session name and session context.
func outputContexts(store session.Store, sessionName string) {
	// Output session name
	if sessionName != "" {
		fmt.Printf("\nSession name: %s\n", sessionName)
	}

	// Output session context from metadata
	if sessionName != "" {
		sess, err := store.Get(sessionName)
		if err == nil && sess.Metadata.Context != "" {
			fmt.Printf("Context: %s\n", sess.Metadata.Context)
		}
	}
}

func init() {
	hookCmd.AddCommand(sessionStartCmd)
}
