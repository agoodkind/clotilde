package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/outputstyle"
	"github.com/fgrehm/clotilde/internal/search"
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

	sessions, err := store.List()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", err)
		os.Exit(1)
	}

	// Build callbacks that bridge ui and cmd packages
	cb := ui.AppCallbacks{
		Store: store,
		ResumeSession: func(sess *session.Session) error {
			return resumeSession(sess, store)
		},
		DeleteSession: func(sess *session.Session) error {
			return deleteSession(sess, store)
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
			settings, _ := store.LoadSettings(sess.Name)
			if settings != nil && settings.Model != "" {
				return settings.Model
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
				settings, _ := store.LoadSettings(sess.Name)
				if settings != nil && settings.Model != "" {
					model = settings.Model
				}
			}

			var msgs []ui.DetailMessage
			if sess.Metadata.TranscriptPath != "" {
				recent := claude.ExtractRecentMessages(sess.Metadata.TranscriptPath, 5, 150)
				for _, m := range recent {
					text := strings.TrimSpace(m.Text)
					if text == "" || strings.HasPrefix(text, "<") || len(text) < 5 {
						continue
					}
					msgs = append(msgs, ui.DetailMessage{Role: m.Role, Text: text})
				}
			}

			return ui.SessionDetail{Model: model, Messages: msgs}
		},
	}

	app := ui.NewApp(sessions, cb)
	app.PreWarmStats()

	if err := app.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}

// runPostSessionDashboard shows the dashboard with "Return to <session>" at the top.
func runPostSessionDashboard(lastSession *session.Session) {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to initialize session storage: %v\n", err)
		return
	}

	for {
		sessions, loadErr := store.List()
		if loadErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", loadErr)
			return
		}
		sortSessionsByLastAccessed(sessions)

		dashboard := ui.NewDashboardPostSession(sessions, lastSession)
		selectedAction, dashErr := ui.RunDashboard(dashboard)
		if dashErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Dashboard error: %v\n", dashErr)
			return
		}

		if selectedAction == "" {
			return
		}

		if selectedAction == "return" {
			// Reload session metadata (may have been updated by auto-context)
			updated, getErr := store.Get(lastSession.Name)
			if getErr == nil {
				lastSession = updated
			}
			lastSession.UpdateLastAccessed()
			_ = store.Update(lastSession)
			if err := resumeSession(lastSession, store); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "Failed to resume: %v\n", err)
			}
			// After returning from session, show this dashboard again
			continue
		}

		shouldReturn := handleDashboardAction(selectedAction, sessions, store)
		if shouldReturn {
			return
		}
	}
}

// handleDashboardAction handles a dashboard action and returns true if we should exit
func handleDashboardAction(selectedAction string, sessions []*session.Session, store session.Store) bool {
	switch selectedAction {
	case "start":
		// Auto-generate a session name and start immediately
		existingNames := make([]string, len(sessions))
		for i, sess := range sessions {
			existingNames[i] = sess.Name
		}
		name := util.GenerateUniqueRandomName(existingNames)

		result, err := createSession(SessionCreateParams{Name: name})
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to create session: %v\n", err)
			os.Exit(1)
		}

		fmt.Println(ui.Success(fmt.Sprintf("Created session '%s' (%s)", result.Session.Name, result.Session.Metadata.SessionID)))
		fmt.Println("\nStarting Claude Code...")

		if err := claude.Start(result.ClotildeRoot, result.Session, result.SettingsFile, nil); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to start session: %v\n", err)
			os.Exit(1)
		}
		return true

	case "resume":
		// Show picker to select session
		if len(sessions) == 0 {
			fmt.Println("No sessions available to resume.")
			return false // Stay in dashboard
		}

		picker := ui.NewPicker(sessions, "Select session to resume").WithPreview()
		picker.PreviewFn = richPreviewFunc(store, picker.StatsCache)
		selected, err := ui.RunPicker(picker)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Picker failed: %v\n", err)
			os.Exit(1)
		}

		if selected == nil {
			// Cancelled - go back to dashboard
			return false
		}

		// Update last accessed
		selected.UpdateLastAccessed()
		if err := store.Update(selected); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to update session: %v\n", err)
			os.Exit(1)
		}

		// Resume the session (reuse logic from resume command)
		if err := resumeSession(selected, store); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to resume session: %v\n", err)
			os.Exit(1)
		}

		// After resuming (launching Claude), exit dashboard
		return true

	case "view":
		if len(sessions) == 0 {
			fmt.Println("No sessions available to view.")
			return false
		}
		viewConversation(sessions, store)
		return false

	case "search":
		if len(sessions) == 0 {
			fmt.Println("No sessions available to search.")
			return false
		}
		searchConversationForm(sessions, store)
		return false

	case "auto-name":
		autoNameSessions(sessions, store)
		return false

	case "fork":
		if len(sessions) == 0 {
			fmt.Println("No sessions available to fork.")
			return false
		}

		// Filter out incognito sessions (can't fork from them)
		var forkable []*session.Session
		for _, s := range sessions {
			if !s.Metadata.IsIncognito {
				forkable = append(forkable, s)
			}
		}
		if len(forkable) == 0 {
			fmt.Println("No non-incognito sessions available to fork.")
			return false
		}

		picker := ui.NewPicker(forkable, "Select session to fork").WithPreview()
		picker.PreviewFn = richPreviewFunc(store, picker.StatsCache)
		parent, err := ui.RunPicker(picker)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Picker failed: %v\n", err)
			os.Exit(1)
		}
		if parent == nil {
			return false
		}

		if err := forkFromDashboard(parent, sessions, store); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to fork session: %v\n", err)
			os.Exit(1)
		}
		return true

	case "delete":
		// Show picker to select session
		if len(sessions) == 0 {
			fmt.Println("No sessions available to delete.")
			return false // Stay in dashboard
		}

		picker := ui.NewPicker(sessions, "Select session to delete").WithPreview()
		selected, err := ui.RunPicker(picker)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Picker failed: %v\n", err)
			os.Exit(1)
		}

		if selected == nil {
			// Cancelled - go back to dashboard
			return false
		}

		// Show confirmation with details
		details := buildDeletionDetails(projectClotildeRootForSession(selected), selected)
		confirmModel := ui.NewConfirm(
			fmt.Sprintf("Delete session '%s'?", selected.Name),
			"This will permanently delete:",
		).WithDetails(details).WithDestructive()

		confirmed, err := ui.RunConfirm(confirmModel)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Confirmation dialog failed: %v\n", err)
			os.Exit(1)
		}

		if !confirmed {
			// Cancelled - go back to dashboard
			return false
		}

		// Delete the session (reuse logic from delete command)
		if err := deleteSession(selected, store); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to delete session: %v\n", err)
			os.Exit(1)
		}

		// After deleting, go back to dashboard
		return false

	case "quit":
		// User explicitly selected quit - exit dashboard
		return true

	default:
		// Unknown action - stay in dashboard
		return false
	}
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

// forkFromDashboard creates a fork with an auto-generated name and launches Claude
func forkFromDashboard(parent *session.Session, sessions []*session.Session, store session.Store) error {
	existingNames := make([]string, len(sessions))
	for i, s := range sessions {
		existingNames[i] = s.Name
	}
	forkName := util.GenerateUniqueRandomName(existingNames)

	globalRoot := config.GlobalDataDir()

	// Create fork session with empty sessionId (filled by hook)
	fork := session.NewSession(forkName, "")
	fork.Metadata.IsForkedSession = true
	fork.Metadata.ParentSession = parent.Name
	fork.Metadata.Context = parent.Metadata.Context
	fork.Metadata.WorkspaceRoot = parent.Metadata.WorkspaceRoot
	if fork.Metadata.WorkspaceRoot == "" {
		fork.Metadata.WorkspaceRoot, _ = config.FindProjectRoot()
	}
	if wd, err := os.Getwd(); err == nil {
		fork.Metadata.WorkDir = wd
	}

	if err := store.Create(fork); err != nil {
		return fmt.Errorf("failed to create fork: %w", err)
	}

	forkDir := config.GetSessionDir(globalRoot, forkName)
	parentDir := config.GetSessionDir(globalRoot, parent.Name)

	// Copy settings.json if exists
	parentSettings := filepath.Join(parentDir, "settings.json")
	if util.FileExists(parentSettings) {
		if err := util.CopyFile(parentSettings, filepath.Join(forkDir, "settings.json")); err != nil {
			return fmt.Errorf("failed to copy settings: %w", err)
		}
	}

	fmt.Println(ui.Success(fmt.Sprintf("Created fork '%s' from '%s'", forkName, parent.Name)))
	fmt.Println("\nStarting Claude Code with fork...")

	var settingsFile string
	if util.FileExists(filepath.Join(forkDir, "settings.json")) {
		settingsFile = filepath.Join(forkDir, "settings.json")
	}

	return claude.Fork(globalRoot, parent, forkName, settingsFile, nil, fork)
}

// viewConversation shows a session picker, then displays the conversation in a scrollable viewer.
func viewConversation(sessions []*session.Session, store session.Store) {
	picker := ui.NewPicker(sessions, "Select session to view").WithPreview()
	picker.PreviewFn = richPreviewFunc(store, picker.StatsCache)
	selected, err := ui.RunPicker(picker)
	if err != nil || selected == nil {
		return
	}

	messages, loadErr := loadSessionMessages(selected)
	if loadErr != nil {
		fmt.Printf("Failed to load conversation: %v\n", loadErr)
		return
	}
	if len(messages) == 0 {
		fmt.Println("No conversation messages found.")
		return
	}

	text := transcript.RenderPlainText(messages)
	viewer := ui.NewViewer(fmt.Sprintf("Conversation: %s", selected.Name), text)
	if err := ui.RunViewer(viewer); err != nil {
		fmt.Printf("Viewer error: %v\n", err)
	}
}

// searchConversationForm shows the unified search form (session + query + depth),
// then runs the search and displays results in the viewer.
func searchConversationForm(sessions []*session.Session, store session.Store) {
	statsCache := make(map[string]*transcript.CompactQuickStats)
	result, err := ui.RunSearchForm(sessions, nil, richPreviewFunc(store, statsCache))
	if err != nil || result == nil || result.Cancelled {
		return
	}

	runSearchAndView(result.Session, result.Query, result.Depth, store)
}

// runSearchAndView runs a search and shows results in the viewer.
func runSearchAndView(selected *session.Session, query, depth string, store session.Store) {
	messages, loadErr := loadSessionMessages(selected)
	if loadErr != nil {
		fmt.Printf("Failed to load conversation: %v\n", loadErr)
		return
	}
	if len(messages) == 0 {
		fmt.Println("No conversation messages found.")
		return
	}

	cfg, _ := config.LoadGlobalOrDefault()
	if depth == "" {
		depth = "quick"
	}

	fmt.Printf("Searching %d messages (%s depth) for: %s\n", len(messages), depth, query)

	ctx := context.Background()
	results, searchErr := search.SearchWithDepth(ctx, messages, query, cfg.Search, depth)
	if searchErr != nil {
		fmt.Printf("Search failed: %v\n", searchErr)
		return
	}

	if len(results) == 0 {
		fmt.Println("No matching messages found.")
		return
	}

	var allMatched []transcript.Message
	for _, r := range results {
		if r.Summary != "" {
			fmt.Printf("  Found: %s\n", r.Summary)
		}
		allMatched = append(allMatched, r.Messages...)
	}

	text := transcript.RenderPlainText(allMatched)
	viewer := ui.NewViewer(fmt.Sprintf("Search results: %q in %s", query, selected.Name), text)
	_ = ui.RunViewer(viewer)
}

// autoNameSessions generates names for sessions via LLM and renames them.
func autoNameSessions(sessions []*session.Session, store session.Store) {
	// Count nameable sessions (non-incognito)
	nameable := 0
	for _, s := range sessions {
		if !s.Metadata.IsIncognito {
			nameable++
		}
	}

	if nameable == 0 {
		fmt.Println("No sessions to rename.")
		return
	}

	confirmModel := ui.NewConfirm(
		fmt.Sprintf("Auto-name %d session(s)?", nameable),
		fmt.Sprintf("Generate names for %d session(s) using claude haiku and rename them.", nameable),
	)
	confirmed, err := ui.RunConfirm(confirmModel)
	if err != nil || !confirmed {
		return
	}

	fmt.Printf("\nGenerating names for %d session(s)...\n", nameable)

	succeeded := 0
	for _, sess := range sessions {
		if sess.Metadata.IsIncognito {
			continue
		}
		name, genErr := generateName(nil, sess, "", nil, "haiku")
		if genErr != nil {
			fmt.Printf("  SKIP %s: %v\n", sess.Name, genErr)
			continue
		}
		if renameErr := store.Rename(sess.Name, name); renameErr != nil {
			fmt.Printf("  FAIL %s: %v\n", sess.Name, renameErr)
			continue
		}
		fmt.Printf("  OK   %s  =>  %s\n", sess.Name, name)
		succeeded++
	}

	fmt.Printf("\nDone. %d/%d sessions renamed.\n", succeeded, nameable)
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
