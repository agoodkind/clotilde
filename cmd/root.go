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
	"github.com/fgrehm/clotilde/internal/outputstyle"
	"github.com/fgrehm/clotilde/internal/session"
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

// runDashboard shows the interactive dashboard when no subcommand is provided
func runDashboard(cmd *cobra.Command, args []string) {
	// Non-interactive (piped) invocation with no subcommand: this is claude's
	// "pipe a prompt" mode (e.g. `echo "query" | claude`). Forward to real claude.
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		os.Exit(ForwardToClaude(os.Args[1:]))
	}

	// Check if in TTY (interactive terminal)
	isTTY := isatty.IsTerminal(os.Stdout.Fd())
	if !isTTY {
		_ = cmd.Help()
		return
	}

	// Use global session store for dashboard
	store, err := session.NewGlobalFileStore()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to initialize session storage: %v\n", err)
		os.Exit(1)
	}

	// Scope dashboard to current workspace
	workspaceRoot, _ := config.FindProjectRoot()

	loadSessions := func() []*session.Session {
		var sessions []*session.Session
		var loadErr error
		if workspaceRoot != "" {
			sessions, loadErr = store.ListForWorkspace(workspaceRoot)
		} else {
			sessions, loadErr = store.List()
		}
		if loadErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to load sessions: %v\n", loadErr)
			os.Exit(1)
		}
		return sessions
	}

	sessions := loadSessions()
	sortSessionsByLastAccessed(sessions)

	// Dashboard loop - keep showing dashboard until quit or session launched
	for {
		sessions = loadSessions()
		sortSessionsByLastAccessed(sessions)

		dashboard := ui.NewDashboard(sessions)
		selectedAction, err := ui.RunDashboard(dashboard)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Dashboard error: %v\n", err)
			os.Exit(1)
		}

		if selectedAction == "" {
			return
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
		picker.PreviewFn = richPreviewFunc(store)
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
		picker.PreviewFn = richPreviewFunc(store)
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

	case "list":
		// Show interactive table
		selected, err := showInteractiveTable(sessions, store)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to show table: %v\n", err)
			os.Exit(1)
		}

		// If no session selected (cancelled), go back to dashboard
		if selected == nil {
			return false
		}

		// Update last accessed
		selected.UpdateLastAccessed()
		if err := store.Update(selected); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to update session: %v\n", err)
			os.Exit(1)
		}

		// Resume the selected session
		if err := resumeSession(selected, store); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Failed to resume session: %v\n", err)
			os.Exit(1)
		}

		// After resuming (launching Claude), exit dashboard
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
	root.AddCommand(newDeleteCmd())
	root.AddCommand(newExportCmd())
	root.AddCommand(newAdoptCmd())
	root.AddCommand(hookCmd)
	root.AddCommand(versionCmd)
	root.AddCommand(newCompletionCmd())
	root.AddCommand(newDaemonCmd())
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
func resumeSession(sess *session.Session, _ session.Store) error {
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
