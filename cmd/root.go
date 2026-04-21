// Package cmd holds the TUI dashboard, its daemon-backed callbacks,
// the `clyde resume` cobra verb, and the argument-routing helpers
// (ClassifyArgs, ForwardToClaude) used by cmd/clyde/main.go to assemble
// the cobra root.
//
// What lives here:
//
//   - RunDashboard / runPostSessionDashboard (the tcell TUI entrypoint)
//   - TUI callbacks for delete, rename, resume, remote-control,
//     bridges, send, tail, registry, summary, view, model extract
//   - NewResumeCmd (the `clyde resume <name|uuid>` verb)
//   - ClassifyArgs and ForwardToClaude (passthrough routing)
//   - resumeSession / deleteSession helpers shared by the TUI and the
//     resume verb
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/claude"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/daemon"
	"goodkind.io/clyde/internal/outputstyle"
	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/transcript"
	"goodkind.io/clyde/internal/ui"
	"goodkind.io/clyde/internal/util"
)

// RunDashboard is the entrypoint for `clyde` with no subcommand. It
// boots the tcell TUI dashboard for managing existing sessions
// (resume, delete, rename, view, remote-control toggle, send-to,
// tail-transcript). New sessions from the TUI launch `claude` with
// CLYDE_SESSION_NAME set; the SessionStart hook adopts the row.
func RunDashboard(cmd *cobra.Command, args []string) {
	// Non-interactive (piped) invocation: forward to real claude.
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		os.Exit(ForwardToClaude(os.Args[1:]))
	}

	// Non-TTY stdout: show help. Avoids drawing the TUI into a pipe.
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		_ = cmd.Help()
		return
	}

	runDashboardTUI()
}

// runDashboardTUI opens the session dashboard. Caller must ensure stdin and
// stdout are TTYs (see RunDashboard).
func runDashboardTUI() {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to initialize session storage: %v\n", err)
		slog.Error("dashboard.store_init_failed",
			"component", "tui",
			slog.Any("err", err),
		)
		os.Exit(1)
	}

	// Adopt orphan transcripts in the background. The daemon also runs
	// this; doing it here makes the dashboard self-healing when the
	// daemon is not yet running.
	go runDiscoveryAdoption(store)

	sessions, err := store.List()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		slog.Error("dashboard.list_failed",
			"component", "tui",
			slog.Any("err", err),
		)
		os.Exit(1)
	}

	slog.Info("dashboard.opened",
		"component", "tui",
		"sessions", len(sessions),
	)

	dashboardCwd, _ := os.Getwd()
	cb := buildAppCallbacks(store, sessions, dashboardCwd)
	app := ui.NewApp(sessions, cb, ui.AppOptions{DashboardLaunchCWD: dashboardCwd})
	app.PreWarmStats()

	if err := app.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		slog.Error("dashboard.tui_error",
			"component", "tui",
			slog.Any("err", err),
		)
		os.Exit(1)
	}
	slog.Info("dashboard.closed", "component", "tui")
}

// runDiscoveryAdoption walks ~/.claude/projects and creates registry
// entries for any transcript whose UUID is unknown. Errors are
// swallowed because this is a best-effort startup task.
func runDiscoveryAdoption(store *session.FileStore) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	projects := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(projects); err != nil {
		return
	}
	results, err := session.ScanProjects(projects)
	if err != nil {
		return
	}
	adopted, err := session.AdoptUnknown(store, results)
	if err != nil {
		return
	}
	if len(adopted) > 0 {
		slog.Info("dashboard.discovery.adopted",
			"component", "tui",
			"count", len(adopted),
		)
		daemon.NudgeDiscoveryScan()
	}
}

// buildAppCallbacks wires store + helpers into a ui.AppCallbacks.
// dashboardLaunchCWD is the process cwd when RunDashboard started; it
// is the default basedir for "new session" without picking a folder.
func buildAppCallbacks(store session.Store, _ []*session.Session, dashboardLaunchCWD string) ui.AppCallbacks {
	return ui.AppCallbacks{
		Store: store,
		StartSessionWithBasedir: func(basedir string) error {
			return startNewSessionInDir(basedir, store, dashboardLaunchCWD)
		},
		ResumeSession: func(sess *session.Session) error {
			return resumeSession(sess, store)
		},
		DeleteSession: func(sess *session.Session) error {
			return deleteSession(sess, store)
		},
		RenameSession: func(sess *session.Session) (string, error) {
			newName := sess.Name
			oldName := sess.Metadata.Name
			if oldName == "" || oldName == newName {
				return newName, nil
			}
			ok, derr := daemon.RenameSessionViaDaemon(context.Background(), oldName, newName)
			if !ok {
				return newName, fmt.Errorf("daemon rename failed: %w", derr)
			}
			return newName, nil
		},
		RefreshSummary: func(sess *session.Session, onDone func(*session.Session)) error {
			return refreshSessionSummary(store, sess, onDone)
		},
		ViewContent: func(sess *session.Session) string {
			messages, err := loadSessionMessages(sess)
			if err != nil || len(messages) == 0 {
				return ""
			}
			return transcript.RenderPlainText(messages)
		},
		ExtractModel: func(sess *session.Session) string {
			if sess.Metadata.TranscriptPath != "" {
				m, _ := claude.ExtractModelAndLastTime(sess.Metadata.TranscriptPath)
				if m != "" {
					return m
				}
			}
			fs, ok := store.(*session.FileStore)
			if ok {
				settings, _ := fs.LoadSettings(sess.Name)
				if settings != nil && settings.Model != "" {
					return settings.Model
				}
			}
			return "-"
		},
		SubscribeRegistry: func() (<-chan ui.SessionEvent, func(), error) {
			raw, cancel, err := daemon.SubscribeRegistry(context.Background())
			if err != nil {
				return nil, nil, err
			}
			out := make(chan ui.SessionEvent, 8)
			go func() {
				defer close(out)
				for ev := range raw {
					out <- ui.SessionEvent{
						Kind:            strings.TrimPrefix(ev.GetKind().String(), "KIND_"),
						SessionName:     ev.GetSessionName(),
						SessionID:       ev.GetSessionId(),
						BridgeSessionID: ev.GetBridgeSessionId(),
						BridgeURL:       ev.GetBridgeUrl(),
					}
				}
			}()
			return out, cancel, nil
		},
		SetRemoteControl: func(sess *session.Session, enabled bool) error {
			if sess == nil || sess.Name == "" {
				return fmt.Errorf("nil session")
			}
			ok, err := daemon.UpdateSessionRemoteControlViaDaemon(context.Background(), sess.Name, enabled)
			if !ok {
				return fmt.Errorf("daemon update failed: %w", err)
			}
			return nil
		},
		SetGlobalRemoteControl: func(enabled bool) error {
			ok, err := daemon.UpdateGlobalRemoteControlViaDaemon(context.Background(), enabled)
			if !ok {
				return fmt.Errorf("daemon update failed: %w", err)
			}
			return nil
		},
		IsRemoteControlEnabled: func(sess *session.Session) bool {
			if sess == nil {
				return false
			}
			fs, ok := store.(*session.FileStore)
			if !ok {
				return false
			}
			settings, _ := fs.LoadSettings(sess.Name)
			return settings != nil && settings.RemoteControl
		},
		IsGlobalRemoteControlEnabled: func() bool {
			cfg, err := config.LoadGlobalOrDefault()
			return err == nil && cfg.Defaults.RemoteControl
		},
		ListBridges: func() ([]ui.Bridge, error) {
			raw, err := daemon.ListBridgesViaDaemon(context.Background())
			if err != nil {
				return nil, err
			}
			out := make([]ui.Bridge, 0, len(raw))
			for _, b := range raw {
				out = append(out, ui.Bridge{
					SessionID:       b.GetSessionId(),
					PID:             b.GetPid(),
					BridgeSessionID: b.GetBridgeSessionId(),
					URL:             b.GetUrl(),
				})
			}
			return out, nil
		},
		SendToSession: func(sessionID, text string) error {
			ok, err := daemon.SendToSessionViaDaemon(context.Background(), sessionID, text)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("session not listening on inject socket")
			}
			return nil
		},
		TailTranscript: func(sessionID string, startOffset int64) (<-chan ui.TranscriptEntry, func(), error) {
			raw, cancel, err := daemon.TailTranscriptViaDaemon(context.Background(), sessionID, startOffset)
			if err != nil {
				return nil, nil, err
			}
			out := make(chan ui.TranscriptEntry, 32)
			go func() {
				defer close(out)
				for ln := range raw {
					ts := time.Time{}
					if ln.GetTimestampNanos() > 0 {
						ts = time.Unix(0, ln.GetTimestampNanos())
					}
					out <- ui.TranscriptEntry{
						ByteOffset: ln.GetByteOffset(),
						RawJSONL:   ln.GetRawJsonl(),
						Role:       ln.GetRole(),
						Text:       ln.GetText(),
						Timestamp:  ts,
					}
				}
			}()
			return out, cancel, nil
		},
		ExtractDetail: func(sess *session.Session) ui.SessionDetail {
			model := "-"
			if sess.Metadata.TranscriptPath != "" {
				m, _ := claude.ExtractModelAndLastTime(sess.Metadata.TranscriptPath)
				if m != "" {
					model = m
				}
			}
			if model == "-" {
				if fs, ok := store.(*session.FileStore); ok {
					settings, _ := fs.LoadSettings(sess.Name)
					if settings != nil && settings.Model != "" {
						model = settings.Model
					}
				}
			}

			var recentMsgs []ui.DetailMessage
			var allMsgs []ui.DetailMessage
			var tools []ui.ToolUse
			if sess.Metadata.TranscriptPath != "" {
				recent := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 5, 150)
				for _, m := range recent {
					text := strings.TrimSpace(m.Text)
					if text == "" || strings.HasPrefix(text, "<") || len(text) < 5 {
						continue
					}
					recentMsgs = append(recentMsgs, ui.DetailMessage{Role: m.Role, Text: text, Timestamp: m.Timestamp})
				}
				all := claude.LoadAllMessages(sess.Metadata.TranscriptPath, 1000)
				for _, m := range all {
					allMsgs = append(allMsgs, ui.DetailMessage{Role: m.Role, Text: m.Text, Timestamp: m.Timestamp})
				}
				for _, t := range claude.ToolUseStats(sess.Metadata.TranscriptPath, 8) {
					tools = append(tools, ui.ToolUse{Name: t.Name, Count: t.Count})
				}
			}
			return ui.SessionDetail{Model: model, Messages: recentMsgs, AllMessages: allMsgs, Tools: tools}
		},
	}
}

// ForwardToClaude runs the real claude binary (bypassing the shell
// alias) with the given args, inheriting stdin/stdout/stderr. Returns
// the exit code. Used by the dispatch path and by RunDashboard's
// piped-input shortcut.
func ForwardToClaude(args []string) int {
	return runClaudeWithEnv(args, os.Environ())
}

func runClaudeWithEnv(args []string, env []string) int {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "clyde: cannot find claude binary: %v\n", err)
		slog.Error("forward.claude_not_found",
			"component", "cli",
			slog.Any("err", err),
		)
		return 1
	}
	slog.Debug("forward.claude.invoked",
		"component", "cli",
		"argc", len(args),
	)
	c := exec.Command(claudePath, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = env
	if runErr := c.Run(); runErr != nil {
		if c.ProcessState != nil {
			return c.ProcessState.ExitCode()
		}
		return 1
	}
	return 0
}

// claudePassthroughFirstArgSkipsPostSessionTUI lists argv[0] values that
// usually run a one-shot or long-lived non-dashboard claude subcommand (see
// claude-code entrypoints/cli.tsx fast paths and main.tsx program.command
// registrations). When clyde forwards to claude and these are the first
// token, skip the post-claude TUI (same intent as api / print).
var claudePassthroughFirstArgSkipsPostSessionTUI = map[string]struct{}{
	"agents":             {},
	"assistant":          {},
	"attach":             {},
	"auth":               {},
	"auto-mode":          {},
	"bridge":             {},
	"daemon":             {},
	"doctor":             {},
	"environment-runner": {},
	"install":            {},
	"kill":               {},
	"logs":               {},
	"plugin":             {},
	"plugins":            {},
	"ps":                 {},
	"rc":                 {},
	"remote":             {},
	"remote-control":     {},
	"self-hosted-runner": {},
	"server":             {},
	"setup-token":        {},
	"sync":               {},
	"update":             {},
	"upgrade":            {},
}

// passthroughSkipsPostSessionTUI reports args that should not open the
// dashboard after claude exits (non-interactive or API-style invocations).
func passthroughSkipsPostSessionTUI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "api" {
		return true
	}
	if _, skip := claudePassthroughFirstArgSkipsPostSessionTUI[args[0]]; skip {
		return true
	}
	for _, a := range args {
		if a == "-p" || a == "--print" {
			return true
		}
	}
	return false
}

// ForwardToClaudeThenDashboard runs claude like ForwardToClaude, but for an
// interactive terminal it assigns CLYDE_SESSION_NAME when unset so the
// SessionStart hook adopts the session, then opens the TUI when claude exits.
// Pipe and print-style invocations behave like ForwardToClaude only.
func ForwardToClaudeThenDashboard(args []string) int {
	if passthroughSkipsPostSessionTUI(args) {
		return ForwardToClaude(args)
	}
	if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
		return ForwardToClaude(args)
	}
	env := os.Environ()
	if os.Getenv("CLYDE_SESSION_NAME") == "" {
		store, serr := session.NewGlobalFileStore()
		if serr == nil {
			name, nerr := nextChatSessionName(store)
			if nerr == nil {
				env = append(env, "CLYDE_SESSION_NAME="+name)
				slog.Info("forward.passthrough_wrapped",
					"component", "cli",
					"session", name,
				)
			}
		}
	}
	_ = runClaudeWithEnv(args, env)
	runDashboardTUI()
	return 0
}

// nextChatSessionName returns a new registry-safe chat-* name that does not
// collide with existing session directories.
func nextChatSessionName(store session.Store) (string, error) {
	list, err := store.List()
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(list))
	for _, s := range list {
		taken[s.Name] = true
	}
	base := "chat-" + time.Now().UTC().Format("20060102-150405")
	name := session.UniqueName(base, taken)
	if taken[name] {
		return "", fmt.Errorf("could not allocate a unique session name")
	}
	return name, nil
}

// startNewSessionInDir launches claude for a new named session in workDir.
// basedir may be empty; dashboardFallbackCWD is used when the trimmed path is empty.
func startNewSessionInDir(basedir string, store session.Store, dashboardFallbackCWD string) error {
	workDir := strings.TrimSpace(basedir)
	if workDir == "" {
		workDir = strings.TrimSpace(dashboardFallbackCWD)
	}
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		workDir = wd
	}

	name, err := nextChatSessionName(store)
	if err != nil {
		return err
	}

	env := map[string]string{"CLYDE_SESSION_NAME": name}
	_, _ = fmt.Fprintf(os.Stdout, "Starting new session %q in %s\n\n", name, workDir)
	slog.Info("session.new.started",
		"component", "cli",
		"session", name,
		"workdir", workDir,
	)

	err = claude.StartNewInteractive(env, "", workDir)
	if err != nil {
		return err
	}
	sess, gerr := store.Get(name)
	if gerr == nil && sess != nil {
		if fs, ok := store.(*session.FileStore); ok {
			autoUpdateContext(fs, sess)
		}
		printResumeInstructions(sess)
	}
	return nil
}

// resumeSession runs claude --resume against an existing clyde
// session, reattaching its workspace add-dir if the user invoked from
// a different cwd. Shared by the resume cobra verb and the TUI
// dashboard's resume callback.
func resumeSession(sess *session.Session, store session.Store) error {
	globalRoot := config.GlobalDataDir()
	sessionDir := config.GetSessionDir(globalRoot, sess.Name)

	var settingsFile string
	settingsPath := filepath.Join(sessionDir, "settings.json")
	if util.FileExists(settingsPath) {
		settingsFile = settingsPath
	}

	var additionalArgs []string
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		if sess.Metadata.WorkspaceRoot != "" && cwd != sess.Metadata.WorkspaceRoot {
			additionalArgs = append(additionalArgs, "--add-dir", cwd)
		}
	}

	_, _ = fmt.Fprintf(os.Stdout, "Resuming session '%s' (%s)\n\n", sess.Name, sess.Metadata.SessionID)
	slog.Info("session.resume.started",
		"component", "cli",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
	)

	err := claude.Resume(globalRoot, sess, settingsFile, additionalArgs)
	if fs, ok := store.(*session.FileStore); ok {
		autoUpdateContext(fs, sess)
	}
	printResumeInstructions(sess)
	return err
}

// deleteSession deletes a session's metadata, its claude transcripts
// and agent logs, and (when present) the per-session output style.
// Prefers the daemon path so subscribers (open dashboards) get the
// SESSION_DELETED event immediately; falls back to the local store
// when the daemon is unreachable.
func deleteSession(sess *session.Session, store session.Store) error {
	allDeletedFiles := &claude.DeletedFiles{
		Transcript: []string{},
		AgentLogs:  []string{},
	}

	projClydeRoot := projectClydeRootForSession(sess)

	deleted, err := claude.DeleteSessionData(projClydeRoot, sess.Metadata.SessionID, sess.Metadata.TranscriptPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stdout, ui.Warning(fmt.Sprintf("Failed to delete Claude data for current session: %v", err)))
		slog.Error("session.delete.current_data_failed",
			"component", "cli",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			slog.Any("err", err),
		)
	} else {
		allDeletedFiles.Transcript = append(allDeletedFiles.Transcript, deleted.Transcript...)
		allDeletedFiles.AgentLogs = append(allDeletedFiles.AgentLogs, deleted.AgentLogs...)
	}

	for _, prevSessionID := range sess.Metadata.PreviousSessionIDs {
		deleted, err := claude.DeleteSessionData(projClydeRoot, prevSessionID, "")
		if err != nil {
			_, _ = fmt.Fprintln(os.Stdout, ui.Warning(fmt.Sprintf("Failed to delete Claude data for previous session %s: %v", prevSessionID, err)))
			slog.Error("session.delete.previous_data_failed",
				"component", "cli",
				"session", sess.Name,
				"previous_session_id", prevSessionID,
				slog.Any("err", err),
			)
		} else {
			allDeletedFiles.Transcript = append(allDeletedFiles.Transcript, deleted.Transcript...)
			allDeletedFiles.AgentLogs = append(allDeletedFiles.AgentLogs, deleted.AgentLogs...)
		}
	}

	if ok, derr := daemon.DeleteSessionViaDaemon(context.Background(), sess.Name); ok {
		_ = derr
	} else if err := store.Delete(sess.Name); err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	if sess.Metadata.HasCustomOutputStyle {
		if err := outputstyle.DeleteCustomStyleFile(config.GlobalOutputStyleRoot(), sess.Name); err != nil {
			_, _ = fmt.Fprintln(os.Stdout, ui.Warning(fmt.Sprintf("Failed to delete output style file: %v", err)))
			slog.Error("session.delete.style_failed",
				"component", "cli",
				"session", sess.Name,
				slog.Any("err", err),
			)
		}
	}

	transcriptCount := len(allDeletedFiles.Transcript)
	agentLogCount := len(allDeletedFiles.AgentLogs)
	_, _ = fmt.Fprintln(os.Stdout, ui.Success(fmt.Sprintf("Deleted session '%s'", sess.Name)))
	_, _ = fmt.Fprintf(os.Stdout, "  Session folder, %d transcript(s), %d agent log(s)\n", transcriptCount, agentLogCount)
	slog.Info("session.deleted",
		"component", "cli",
		"session", sess.Name,
		"transcripts", transcriptCount,
		"agent_logs", agentLogCount,
	)

	return nil
}

// loadSessionMessages loads parsed messages from all transcripts for
// a session. Used by the TUI's ViewContent callback.
func loadSessionMessages(sess *session.Session) ([]transcript.Message, error) {
	homeDir, err := util.HomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}
	clydeRoot := projectClydeRootForSession(sess)
	paths := allTranscriptPaths(sess, clydeRoot, homeDir)

	var allMessages []transcript.Message
	for _, path := range paths {
		f, openErr := os.Open(path)
		if openErr != nil {
			continue
		}
		messages, parseErr := transcript.Parse(f)
		_ = f.Close()
		if parseErr != nil {
			continue
		}
		allMessages = append(allMessages, messages...)
	}
	return allMessages, nil
}
