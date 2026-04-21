package claude

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/daemon"
	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/util"
)

// VerboseFunc is a function that returns whether verbose mode is enabled.
// This is set by the cmd package.
var VerboseFunc func() bool = func() bool { return false }

// SessionUsedFunc checks if a Claude Code session was actually used (has a transcript).
// Can be overridden in tests where the fake claude binary doesn't create transcripts.
var SessionUsedFunc = DefaultSessionUsed

// appendCommonArgs adds settings flags and global defaults to the arg list.
func appendCommonArgs(args []string, settingsFile string) []string {
	if settingsFile != "" && util.FileExists(settingsFile) {
		args = append(args, "--settings", settingsFile)
	}
	if remoteControlEnabled(settingsFile) {
		args = append(args, "--remote-control")
	}
	return args
}

// remoteControlEnabled decides whether to pass --remote-control to
// claude. Per session settings.json wins. The global config default
// fills in when the session has no explicit value. The two layers
// allow a user to opt one session in without forcing the flag on
// every other session.
func remoteControlEnabled(settingsFile string) bool {
	if settingsFile != "" && util.FileExists(settingsFile) {
		var s session.Settings
		if err := util.ReadJSON(settingsFile, &s); err == nil && s.RemoteControl {
			return true
		}
	}
	cfg, err := config.LoadGlobalOrDefault()
	return err == nil && cfg.Defaults.RemoteControl
}

// Resume invokes claude CLI to resume an existing session.
func Resume(clydeRoot string, sess *session.Session, settingsFile string, additionalArgs []string) error {
	args := []string{"--resume", sess.Metadata.SessionID, "-n", sess.Name}
	args = appendCommonArgs(args, settingsFile)
	args = append(args, additionalArgs...)

	env := map[string]string{
		"CLYDE_SESSION_NAME": sess.Name,
	}

	if sess.Metadata.IsIncognito {
		return invokeWithCleanup(clydeRoot, sess, args, env, sess.Metadata.WorkDir)
	}

	if remoteControlEnabled(settingsFile) {
		return invokeInteractivePTY(args, env, sess.Metadata.WorkDir, sess.Metadata.SessionID)
	}
	return invokeInteractive(args, env, sess.Metadata.WorkDir)
}

// StartNewInteractive runs claude without --resume for a new named session.
// env must set CLYDE_SESSION_NAME so the SessionStart hook can adopt the row.
// settingsFile may be empty; remote-control and settings injection match Resume.
func StartNewInteractive(env map[string]string, settingsFile string, workDir string) error {
	args := []string{}
	args = appendCommonArgs(args, settingsFile)
	if remoteControlEnabled(settingsFile) {
		return invokeInteractivePTY(args, env, workDir, "")
	}
	return invokeInteractive(args, env, workDir)
}

// ClaudeBinaryPathFunc is a function that returns the path to the claude binary.
// This is set by the cmd package to allow overriding for tests.
var ClaudeBinaryPathFunc func() string = func() string { return "claude" }

// displayCommand prints the command being executed (always shown) and verbose debug info (if verbose mode).
func displayCommand(claudeBin string, args []string, env map[string]string) {
	// Always display the command being executed
	cmdStr := claudeBin + " " + strings.Join(args, " ")
	fmt.Fprintf(os.Stderr, "→ %s\n", cmdStr)

	// Show additional debug info in verbose mode
	if VerboseFunc() {
		if len(env) > 0 {
			fmt.Fprintln(os.Stderr, "[DEBUG] Environment variables:")
			for k, v := range env {
				fmt.Fprintf(os.Stderr, "  %s=%s\n", k, v)
			}
		}
	}
}

// invokeInteractive executes the claude CLI command interactively.
// Stdin, stdout, and stderr are connected to the current process.
// If the daemon is reachable, it acquires a per-session settings file
// for model isolation and injects --settings. If the daemon is not
// running, claude is invoked directly (graceful degradation).
// workDir, if non-empty, sets the working directory for the subprocess.
func invokeInteractive(args []string, env map[string]string, workDir string) error {
	claudeBin := ClaudeBinaryPathFunc()

	// Try to connect to daemon for per-session model isolation.
	// If the daemon is not running, skip (non-fatal).
	ctx := context.Background()
	wrapperID := fmt.Sprintf("%d", os.Getpid())
	sessionName := env["CLYDE_SESSION_NAME"]

	client, err := daemon.ConnectOrStart(ctx)
	if err == nil {
		resp, acqErr := client.AcquireSession(wrapperID, sessionName)
		if acqErr == nil {
			// Inject per-session settings before other args.
			args = append([]string{"--settings", resp.SettingsFile}, args...)
		} else if VerboseFunc() {
			fmt.Fprintf(os.Stderr, "[DEBUG] daemon acquire failed: %v\n", acqErr)
		}
		// Close initial connection  --  the monitor goroutine manages its own.
		client.Close()
	} else if VerboseFunc() {
		fmt.Fprintf(os.Stderr, "[DEBUG] daemon not available: %v\n", err)
	}

	displayCommand(claudeBin, args, env)

	cmd := exec.Command(claudeBin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Restore working directory if stored (empty = inherit from parent process)
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Set environment variables
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", key, value))
	}

	// Start a background goroutine that monitors the daemon connection.
	// If the daemon restarts (e.g. after `make install`), this re-registers
	// the session with the new daemon so global settings sync continues.
	done := make(chan struct{})
	go monitorDaemon(ctx, wrapperID, sessionName, done)

	runErr := cmd.Run()

	// Signal the monitor to stop and release the session.
	close(done)

	return runErr
}

// monitorDaemon runs alongside claude, periodically checking the daemon
// connection. If the daemon restarted (kill + relaunch during deploy),
// this re-acquires the session so global settings sync keeps working.
// On done signal, releases the session from whichever daemon is current.
func monitorDaemon(ctx context.Context, wrapperID, sessionName string, done <-chan struct{}) {
	const interval = 30 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			// Session ended  --  release from current daemon.
			c, err := daemon.ConnectOrStart(ctx)
			if err == nil {
				_ = c.ReleaseSession(wrapperID)
				c.Close()
			}
			return

		case <-ticker.C:
			// Health check: try to connect and re-acquire.
			// ConnectOrStart is idempotent (flock prevents double-start).
			// AcquireSession is idempotent (daemon overwrites existing entry).
			c, err := daemon.ConnectOrStart(ctx)
			if err != nil {
				if VerboseFunc() {
					fmt.Fprintf(os.Stderr, "[DEBUG] daemon monitor: connect failed: %v\n", err)
				}
				continue
			}
			_, acqErr := c.AcquireSession(wrapperID, sessionName)
			if acqErr != nil && VerboseFunc() {
				fmt.Fprintf(os.Stderr, "[DEBUG] daemon monitor: re-acquire failed: %v\n", acqErr)
			}
			c.Close()
		}
	}
}

// ResumeByName invokes claude with --resume <name>, letting Claude resolve
// the display name to a session UUID internally. Used when clyde doesn't
// have the session in its own store. The daemon wrapping in invokeInteractive
// still provides model isolation.
func ResumeByName(name string, additionalArgs []string) error {
	args := []string{"--resume", name}
	args = append(args, additionalArgs...)
	env := map[string]string{
		"CLYDE_SESSION_NAME": name,
	}
	return invokeInteractive(args, env, "")
}

// invokeWithCleanup runs claude and cleans up incognito session on exit.
// Uses defer to ensure cleanup runs even on panic or interrupt (Ctrl+C).
func invokeWithCleanup(clydeRoot string, sess *session.Session, args []string, env map[string]string, workDir string) error {
	// Setup cleanup to run after claude exits (even on panic/Ctrl+C)
	defer func() {
		deleted, err := cleanupIncognitoSession(clydeRoot, sess)
		if err != nil {
			slog.Warn("claude.incognito.cleanup.failed", "session", sess.Name, slog.Any("err", err))
		} else {
			slog.Info("claude.incognito.deleted", "session", sess.Name, "transcript_count", len(deleted.Transcript), "agent_log_count", len(deleted.AgentLogs))

			// Show detailed info in verbose mode
			if VerboseFunc() {
				transcriptCount := len(deleted.Transcript)
				agentLogCount := len(deleted.AgentLogs)
				slog.Debug("claude.incognito.cleanup.details",
					"session", sess.Name,
					"transcripts", transcriptCount,
					"agent_logs", agentLogCount,
					"transcript_paths", deleted.Transcript,
					"agent_log_paths", deleted.AgentLogs,
				)
			}
		}
	}()

	// Run claude (blocks until exit)
	return invokeInteractive(args, env, workDir)
}

// cleanupIncognitoSession deletes session folder and Claude data.
// Returns DeletedFiles with info about what was deleted.
func cleanupIncognitoSession(clydeRoot string, sess *session.Session) (*DeletedFiles, error) {
	deleted := &DeletedFiles{
		Transcript: []string{},
		AgentLogs:  []string{},
	}

	// Delete Claude data (transcript + agent logs)
	claudeDeleted, err := DeleteSessionData(clydeRoot, sess.Metadata.SessionID, sess.Metadata.TranscriptPath)
	if err != nil {
		// Log but continue - session folder cleanup is more important
		fmt.Fprintf(os.Stderr, "Warning: failed to delete Claude data: %v\n", err)
	} else {
		deleted.Transcript = append(deleted.Transcript, claudeDeleted.Transcript...)
		deleted.AgentLogs = append(deleted.AgentLogs, claudeDeleted.AgentLogs...)
	}

	// Delete session folder
	store := session.NewFileStore(clydeRoot)
	if err := store.Delete(sess.Name); err != nil {
		return deleted, err
	}

	return deleted, nil
}

// defaultSessionUsed checks if a Claude Code session was actually used by looking
// for a transcript file. Sessions with no ID are considered unused.
func DefaultSessionUsed(globalRoot string, sess *session.Session) bool {
	sessionID := sess.Metadata.SessionID
	if sessionID == "" {
		return false
	}

	// Prefer the transcript path saved by the hook (accurate even with symlinks).
	if sess.Metadata.TranscriptPath != "" {
		return util.FileExists(sess.Metadata.TranscriptPath)
	}

	homeDir, err := util.HomeDir()
	if err != nil {
		return true // assume used if we can't check
	}

	// Derive project-specific clyde root from WorkspaceRoot in session metadata.
	// Sessions are stored globally, but transcripts live under the project directory.
	clydeRoot := globalRoot
	if sess.Metadata.WorkspaceRoot != "" {
		clydeRoot = filepath.Join(sess.Metadata.WorkspaceRoot, config.ClydeDir)
	}

	transcriptPath := TranscriptPath(homeDir, clydeRoot, sessionID)
	return util.FileExists(transcriptPath)
}

