package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/daemon"
	"github.com/fgrehm/clotilde/internal/outputstyle"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/fgrehm/clotilde/internal/util"
)

var rootCmd = &cobra.Command{
	Use:     "clotilde",
	Short:   "Named sessions, profiles, and context management for Claude Code",
	Long:    `Clotilde wraps Claude Code with human-friendly session names, profiles, and context management, enabling easy switching between multiple parallel conversations.`,
	Version: version,
	Run:     runDashboard,
}

// runDashboard shows the interactive tview TUI when no subcommand is provided
func runDashboard(cmd *cobra.Command, args []string) {
	// Non-interactive (piped) invocation: forward to real claude.
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		os.Exit(ForwardToClaude(os.Args[1:]))
	}

	// Non-TTY: show help
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		_ = cmd.Help()
		return
	}

	store, err := session.NewGlobalFileStore()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to initialize session storage: %v\n", err)
		os.Exit(1)
	}

	// Adopt orphan transcripts in the background. The daemon also runs
	// this, but doing it here makes the dashboard self-healing even when
	// the daemon is not running yet. The result is picked up by the
	// existing store watcher inside the TUI.
	go runDiscoveryAdoption(store)

	sessions, err := store.List()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	cb := buildAppCallbacks(store, sessions)
	app := ui.NewApp(sessions, cb)
	app.PreWarmStats()

	if err := app.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

// runDiscoveryAdoption walks ~/.claude/projects and creates registry
// entries for any transcript whose UUID is unknown. Errors are swallowed
// because this runs as a best-effort startup task; the user only sees
// the result indirectly through the dashboard listing.
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
	_, _ = session.AdoptUnknown(store, results)
}

// runPostSessionDashboard shows the main tcell TUI after a session exits,
// with a "Return to <name>" prompt preselected so a single Enter resumes
// the previous session. Press Down then Enter from the prompt to quit.
// Press Esc to dismiss the prompt and browse the full session list.
func runPostSessionDashboard(lastSession *session.Session) {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to initialize session storage: %v\n", err)
		return
	}
	sessions, err := store.List()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		return
	}
	// Refresh lastSession metadata so the header banner reflects any
	// auto-context update that ran in the background.
	if updated, getErr := store.Get(lastSession.Name); getErr == nil {
		lastSession = updated
	}

	cb := buildAppCallbacks(store, sessions)
	app := ui.NewApp(sessions, cb, ui.AppOptions{ReturnTo: lastSession})
	app.PreWarmStats()
	if runErr := app.Run(); runErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "TUI error: %v\n", runErr)
	}
}

// buildAppCallbacks wires store + helpers into a ui.AppCallbacks. Shared by
// runDashboard and runPostSessionDashboard so both entry points use the same
// main TUI.
func buildAppCallbacks(store session.Store, sessions []*session.Session) ui.AppCallbacks {
	return ui.AppCallbacks{
		Store: store,
		ResumeSession: func(sess *session.Session) error {
			return resumeSession(sess, store)
		},
		DeleteSession: func(sess *session.Session) error {
			return deleteSession(sess, store)
		},
		ApplyCompact: func(sess *session.Session, choices ui.CompactChoices) error {
			return applyCompactChoices(sess, choices)
		},
		SetBasedir: func(sess *session.Session, newPath string) error {
			resolved, err := resolveBasedirArg(newPath)
			if err != nil {
				return err
			}
			sess.Metadata.WorkspaceRoot = resolved
			return store.Update(sess)
		},
		RefreshSummary: func(sess *session.Session, onDone func(*session.Session)) error {
			return refreshSessionSummary(store, sess, onDone)
		},
		StartSession: func() error {
			return startInteractiveSession(sessions, "")
		},
		StartSessionWithBasedir: func(basedir string) error {
			resolved, err := resolveBasedirArg(basedir)
			if err != nil {
				return err
			}
			return startInteractiveSession(sessions, resolved)
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

// startInteractiveSession creates a new clotilde session and launches
// claude. When basedir is non-empty it lands in WorkspaceRoot so the
// dashboard groups the session with its project even if the launching
// terminal sits in another directory.
func startInteractiveSession(existing []*session.Session, basedir string) error {
	existingNames := make([]string, len(existing))
	for i, s := range existing {
		existingNames[i] = s.Name
	}
	name := util.GenerateUniqueRandomName(existingNames)

	result, err := createSession(SessionCreateParams{Name: name})
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	if basedir != "" {
		result.Session.Metadata.WorkspaceRoot = basedir
		result.Session.Metadata.WorkDir = basedir
		store, sErr := session.NewGlobalFileStore()
		if sErr == nil {
			_ = store.Update(result.Session)
		}
	}

	// Tell the daemon a new session exists so its scanner refreshes
	// its registry view immediately. The nudge is best-effort; the
	// background tick still catches anything that slips through.
	daemon.NudgeDiscoveryScan()

	fmt.Println(ui.Success(fmt.Sprintf("Created session '%s' (%s)", result.Session.Name, result.Session.Metadata.SessionID)))
	fmt.Println("\nStarting Claude Code...")

	return claude.Start(result.ClotildeRoot, result.Session, result.SettingsFile, nil)
}

func init() {
	// Disable Cobra's auto-generated completion command so we can use our custom one
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// Initialize global rootCmd with all subcommands
	initRootCmd()
}

// initRootCmd initializes the global rootCmd with all subcommands
func initRootCmd() {
	registerSubcommands(rootCmd)
}

// claudeBinaryPath is set via the --claude-bin flag (hidden, for testing)
var claudeBinaryPath string

// verbose is set via the --verbose/-v flag
var verbose bool

// NewRootCmd returns a new root command instance (useful for testing).
// Creates a fresh command tree to avoid flag pollution between tests.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     rootCmd.Use,
		Short:   rootCmd.Short,
		Long:    rootCmd.Long,
		Version: rootCmd.Version,
	}
	root.CompletionOptions.DisableDefaultCmd = true
	registerSubcommands(root)
	return root
}

// registerSubcommands adds all subcommands and global flags to the given root command.
func registerSubcommands(root *cobra.Command) {
	freshInitCmd := &cobra.Command{
		Use:   initCmd.Use,
		Short: initCmd.Short,
		Long:  initCmd.Long,
		RunE:  initCmd.RunE,
	}
	freshInitCmd.Flags().Bool("global", false, "Install hooks in .claude/settings.json (project-wide) instead of settings.local.json (local)")

	root.AddCommand(freshInitCmd)
	root.AddCommand(newSetupCmd())
	root.AddCommand(newStartCmd())
	root.AddCommand(newIncognitoCmd())
	root.AddCommand(newResumeCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newInspectCmd())
	root.AddCommand(newForkCmd())
	root.AddCommand(newRenameCmd())
	root.AddCommand(newAutoNameCmd())
	root.AddCommand(newBenchEmbedCmd())
	root.AddCommand(newCompactCmd())
	root.AddCommand(newDeleteCmd())
	root.AddCommand(newPruneEphemeralCmd())
	root.AddCommand(newPruneEmptyCmd())
	root.AddCommand(newPruneAutonameCmd())
	root.AddCommand(newSetBasedirCmd())
	root.AddCommand(newExportCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newAdoptCmd())
	root.AddCommand(hookCmd)
	root.AddCommand(versionCmd)
	root.AddCommand(newCompletionCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newExecCmd())

	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	root.PersistentFlags().StringVar(&claudeBinaryPath, "claude-bin", "", "Path to claude binary (hidden, for testing)")
	_ = root.PersistentFlags().MarkHidden("claude-bin")
}

// GetClaudeBinaryPath returns the path to the claude binary.
// If --claude-bin flag is set, returns that path. Otherwise returns "claude".
func GetClaudeBinaryPath() string {
	if claudeBinaryPath != "" {
		return claudeBinaryPath
	}
	return "claude"
}

// IsVerbose returns whether verbose mode is enabled.
func IsVerbose() bool {
	return verbose
}

func Execute() {
	// Suppress cobra's own error printing so we can handle passthrough ourselves.
	rootCmd.SilenceErrors = true

	if err := rootCmd.Execute(); err != nil {
		// Unknown subcommand: forward transparently to the real claude binary.
		// This handles internal Claude Code commands (e.g. "claude exec ...") that
		// the shell alias routes through clotilde but that clotilde doesn't own.
		if strings.HasPrefix(err.Error(), "unknown command") {
			os.Exit(ForwardToClaude(os.Args[1:]))
		}
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// ForwardToClaude runs the real claude binary (bypassing the shell function) with
// the given args, inheriting stdin/stdout/stderr. Returns the exit code.
func ForwardToClaude(args []string) int {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "clotilde: cannot find claude binary: %v\n", err)
		return 1
	}
	cmd := exec.Command(claudePath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode()
		}
		return 1
	}
	return 0
}

// resumeSession resumes a session (extracted from resume command)
func resumeSession(sess *session.Session, store session.Store) error {
	globalRoot := config.GlobalDataDir()
	sessionDir := config.GetSessionDir(globalRoot, sess.Name)

	var settingsFile string
	settingsPath := filepath.Join(sessionDir, "settings.json")
	if util.FileExists(settingsPath) {
		settingsFile = settingsPath
	}

	// Auto add-dir if resuming from a different directory
	var additionalArgs []string
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		if sess.Metadata.WorkspaceRoot != "" && cwd != sess.Metadata.WorkspaceRoot {
			additionalArgs = append(additionalArgs, "--add-dir", cwd)
		}
	}

	fmt.Printf("Resuming session '%s' (%s)\n\n", sess.Name, sess.Metadata.SessionID)

	err := claude.Resume(globalRoot, sess, settingsFile, additionalArgs)
	if fs, ok := store.(*session.FileStore); ok {
		autoUpdateContext(fs, sess)
	}
	printResumeInstructions(sess)
	return err
}

// deleteSession deletes a session (extracted from delete command)
func deleteSession(sess *session.Session, store session.Store) error {
	// Track all deleted files for verbose output
	allDeletedFiles := &claude.DeletedFiles{
		Transcript: []string{},
		AgentLogs:  []string{},
	}

	// Use project-level clotilde root for transcript/agent-log path computation
	projClotildeRoot := projectClotildeRootForSession(sess)

	// Delete Claude data for current session (transcript and agent logs)
	deleted, err := claude.DeleteSessionData(projClotildeRoot, sess.Metadata.SessionID, sess.Metadata.TranscriptPath)
	if err != nil {
		fmt.Println(ui.Warning(fmt.Sprintf("Failed to delete Claude data for current session: %v", err)))
	} else {
		allDeletedFiles.Transcript = append(allDeletedFiles.Transcript, deleted.Transcript...)
		allDeletedFiles.AgentLogs = append(allDeletedFiles.AgentLogs, deleted.AgentLogs...)
	}

	// Delete Claude data for previous sessions (from /clear operations, and defensively from /compact)
	for _, prevSessionID := range sess.Metadata.PreviousSessionIDs {
		deleted, err := claude.DeleteSessionData(projClotildeRoot, prevSessionID, "")
		if err != nil {
			fmt.Println(ui.Warning(fmt.Sprintf("Failed to delete Claude data for previous session %s: %v", prevSessionID, err)))
		} else {
			allDeletedFiles.Transcript = append(allDeletedFiles.Transcript, deleted.Transcript...)
			allDeletedFiles.AgentLogs = append(allDeletedFiles.AgentLogs, deleted.AgentLogs...)
		}
	}

	// Delete session folder
	if err := store.Delete(sess.Name); err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	// Delete custom output style if it exists
	if sess.Metadata.HasCustomOutputStyle {
		if err := outputstyle.DeleteCustomStyleFile(config.GlobalOutputStyleRoot(), sess.Name); err != nil {
			fmt.Println(ui.Warning(fmt.Sprintf("Failed to delete output style file: %v", err)))
		}
	}

	// Show summary of what was deleted
	transcriptCount := len(allDeletedFiles.Transcript)
	agentLogCount := len(allDeletedFiles.AgentLogs)
	fmt.Println(ui.Success(fmt.Sprintf("Deleted session '%s'", sess.Name)))
	fmt.Printf("  Session folder, %d transcript(s), %d agent log(s)\n", transcriptCount, agentLogCount)

	// Show detailed file paths in verbose mode
	if verbose {
		if transcriptCount > 0 {
			fmt.Println("\n  Deleted transcripts:")
			for _, path := range allDeletedFiles.Transcript {
				fmt.Printf("    %s\n", path)
			}
		}
		if agentLogCount > 0 {
			fmt.Println("\n  Deleted agent logs:")
			for _, path := range allDeletedFiles.AgentLogs {
				fmt.Printf("    %s\n", path)
			}
		}
	}

	return nil
}


// loadSessionMessages loads parsed messages from all transcripts for a session.
func loadSessionMessages(sess *session.Session) ([]transcript.Message, error) {
	homeDir, err := util.HomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}

	clotildeRoot := projectClotildeRootForSession(sess)
	paths := allTranscriptPaths(sess, clotildeRoot, homeDir)

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
