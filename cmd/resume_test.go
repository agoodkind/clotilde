package cmd_test

import (
	"io"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fgrehm/clotilde/cmd"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/testutil"
)

var _ = Describe("Resume Command", func() {
	var (
		tempDir        string
		clotildeRoot   string
		originalWd     string
		claudeArgsFile string
		fakeClaudeDir  string
		store          session.Store
	)

	BeforeEach(func() {
		// Create temp directory
		tempDir = GinkgoT().TempDir()

		// Save original working directory
		var err error
		originalWd, err = os.Getwd()
		Expect(err).NotTo(HaveOccurred())

		// Change to temp directory
		err = os.Chdir(tempDir)
		Expect(err).NotTo(HaveOccurred())

		// Setup fake claude binary
		fakeClaudeDir = filepath.Join(tempDir, "bin")
		err = os.Mkdir(fakeClaudeDir, 0o755)
		Expect(err).NotTo(HaveOccurred())

		_, claudeArgsFile, err = testutil.CreateFakeClaude(fakeClaudeDir)
		Expect(err).NotTo(HaveOccurred())

		Expect(err).NotTo(HaveOccurred())

		// Initialize clotilde
		err = config.EnsureClotildeStructure(tempDir)
		Expect(err).NotTo(HaveOccurred())

		clotildeRoot = filepath.Join(tempDir, config.ClotildeDir)
		store = session.NewFileStore(clotildeRoot)
	})

	AfterEach(func() {
		// Restore PATH

		// Restore working directory
		_ = os.Chdir(originalWd)
	})

	It("should resume an existing session and update lastAccessed", func() {
		// Create a session first
		sess := session.NewSession("test-session", "test-uuid-123")
		err := store.Create(sess)
		Expect(err).NotTo(HaveOccurred())

		// Store original lastAccessed time
		originalLastAccessed := sess.Metadata.LastAccessed

		// Execute resume command
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"--claude-bin", filepath.Join(fakeClaudeDir, "claude"), "resume", "test-session"})

		err = rootCmd.Execute()
		Expect(err).NotTo(HaveOccurred())

		// Verify session was updated
		updatedSess, err := store.Get("test-session")
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedSess.Metadata.LastAccessed).To(BeTemporally(">", originalLastAccessed))

		// Verify claude was invoked with --resume
		args, err := testutil.ReadClaudeArgs(claudeArgsFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(args).To(ContainSubstring("--resume"))
		Expect(args).To(ContainSubstring("test-uuid-123"))
	})

	It("should pass settings file if it exists", func() {
		// Create a session with settings
		sess := session.NewSession("with-settings", "uuid-456")
		err := store.Create(sess)
		Expect(err).NotTo(HaveOccurred())

		settings := &session.Settings{
			Model: "opus",
		}
		err = store.SaveSettings("with-settings", settings)
		Expect(err).NotTo(HaveOccurred())

		// Execute resume command
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"--claude-bin", filepath.Join(fakeClaudeDir, "claude"), "resume", "with-settings"})

		err = rootCmd.Execute()
		Expect(err).NotTo(HaveOccurred())

		// Verify claude was invoked with --settings
		args, err := testutil.ReadClaudeArgs(claudeArgsFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(args).To(ContainSubstring("--settings"))
		Expect(args).To(ContainSubstring("settings.json"))
	})

	It("should not pass settings if they don't exist", func() {
		// Create a minimal session
		sess := session.NewSession("minimal", "uuid-minimal")
		err := store.Create(sess)
		Expect(err).NotTo(HaveOccurred())

		// Execute resume command
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"--claude-bin", filepath.Join(fakeClaudeDir, "claude"), "resume", "minimal"})

		err = rootCmd.Execute()
		Expect(err).NotTo(HaveOccurred())

		// Verify claude was invoked WITHOUT optional flags
		args, err := testutil.ReadClaudeArgs(claudeArgsFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(args).NotTo(ContainSubstring("--settings"))
	})

	It("should return error for non-existent session", func() {
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"--claude-bin", filepath.Join(fakeClaudeDir, "claude"), "resume", "does-not-exist"})

		err := rootCmd.Execute()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not found"))
	})
})
