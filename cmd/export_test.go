package cmd_test

import (
	"bytes"
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

var _ = Describe("Export Command", func() {
	var (
		tempDir      string
		clotildeRoot string
		originalWd   string
		store        session.Store
	)

	BeforeEach(func() {
		tempDir = GinkgoT().TempDir()

		var err error
		originalWd, err = os.Getwd()
		Expect(err).NotTo(HaveOccurred())

		err = os.Chdir(tempDir)
		Expect(err).NotTo(HaveOccurred())

		fakeClaudeDir := filepath.Join(tempDir, "bin")
		err = os.Mkdir(fakeClaudeDir, 0o755)
		Expect(err).NotTo(HaveOccurred())

		_, _, err = testutil.CreateFakeClaude(fakeClaudeDir)
		Expect(err).NotTo(HaveOccurred())

		err = config.EnsureClotildeStructure(tempDir)
		Expect(err).NotTo(HaveOccurred())

		clotildeRoot = filepath.Join(tempDir, config.ClotildeDir)
		store = session.NewFileStore(clotildeRoot)
	})

	AfterEach(func() {
		_ = os.Chdir(originalWd)
	})

	createSessionWithTranscript := func(name, uuid string) {
		sess := session.NewSession(name, uuid)
		transcriptPath := filepath.Join(tempDir, uuid+".jsonl")
		transcriptData := `{"type":"user","timestamp":"2025-01-01T00:01:00Z","message":{"content":"hello"}}
{"type":"assistant","timestamp":"2025-01-01T00:01:05Z","message":{"content":[{"type":"text","text":"hi"}]}}
`
		err := os.WriteFile(transcriptPath, []byte(transcriptData), 0o644)
		Expect(err).NotTo(HaveOccurred())
		sess.Metadata.TranscriptPath = transcriptPath
		err = store.Create(sess)
		Expect(err).NotTo(HaveOccurred())
	}

	It("writes HTML file to working directory with default name", func() {
		createSessionWithTranscript("my-session", "uuid-123")

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"export", "my-session"})

		err := rootCmd.Execute()
		Expect(err).NotTo(HaveOccurred())

		outputPath := filepath.Join(tempDir, "my-session.html")
		content, err := os.ReadFile(outputPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("<!DOCTYPE html>"))
		Expect(string(content)).To(ContainSubstring("my-session"))
	})

	It("writes to custom output path with -o flag", func() {
		createSessionWithTranscript("my-session", "uuid-456")

		outputPath := filepath.Join(tempDir, "custom.html")
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"export", "my-session", "-o", outputPath})

		err := rootCmd.Execute()
		Expect(err).NotTo(HaveOccurred())

		content, err := os.ReadFile(outputPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring("<!DOCTYPE html>"))
	})

	It("writes HTML to stdout with --stdout flag", func() {
		createSessionWithTranscript("my-session", "uuid-789")

		var buf bytes.Buffer
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"export", "my-session", "--stdout"})

		err := rootCmd.Execute()
		Expect(err).NotTo(HaveOccurred())
		Expect(buf.String()).To(ContainSubstring("<!DOCTYPE html>"))

		// No file should be created
		_, err = os.Stat(filepath.Join(tempDir, "my-session.html"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("returns error for non-existent session", func() {
		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"export", "does-not-exist"})

		err := rootCmd.Execute()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not found"))
	})

	It("returns error when transcript is missing", func() {
		sess := session.NewSession("no-transcript", "uuid-missing")
		sess.Metadata.TranscriptPath = "/nonexistent/path.jsonl"
		err := store.Create(sess)
		Expect(err).NotTo(HaveOccurred())

		rootCmd := cmd.NewRootCmd()
		rootCmd.SetOut(io.Discard)
		rootCmd.SetErr(io.Discard)
		rootCmd.SetArgs([]string{"export", "no-transcript"})

		err = rootCmd.Execute()
		Expect(err).To(HaveOccurred())
	})
})
