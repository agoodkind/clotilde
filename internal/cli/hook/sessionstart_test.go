package hook_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/cli"
	"goodkind.io/clyde/internal/cli/hook"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/notify"
	"goodkind.io/clyde/internal/session"
)

func createFakeClaude(dir string) (string, string, error) {
	binaryPath := filepath.Join(dir, "claude")
	argsFile := filepath.Join(dir, "claude-args.txt")
	script := "#!/bin/bash\n\techo \"$@\" > " + argsFile + "\n\texit 0\n"
	if err := os.WriteFile(binaryPath, []byte(script), 0o755); err != nil {
		return "", "", err
	}
	return binaryPath, argsFile, nil
}

// executeHookWithInput executes a hook command with JSON input via stdin.
func executeHookWithInput(hookName string, input []byte) error { //nolint:unparam // test helper, hookName kept for clarity
	oldStdin := os.Stdin
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		return pipeErr
	}
	os.Stdin = r

	go func() {
		defer func() { _ = w.Close() }()
		_, _ = w.Write(input)
	}()

	root := &cobra.Command{Use: "clyde"}
	cli.RegisterGlobalFlags(root)
	f := cli.NewSystemFactory(cli.BuildInfo{Version: "DEVELOPMENT"})
	root.AddCommand(hook.NewCmd(f))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"hook", hookName})
	execErr := root.Execute()

	os.Stdin = oldStdin
	return execErr
}

var _ = Describe("Hook Commands", func() {
	var (
		tempDir        string
		clydeRoot      string
		originalWd     string
		originalLogDir string
		originalXDG    string
		notifyLogDir   string
		store          session.Store
	)

	BeforeEach(func() {
		tempDir = GinkgoT().TempDir()

		var err error
		originalWd, err = os.Getwd()
		Expect(err).NotTo(HaveOccurred())
		originalXDG = os.Getenv("XDG_DATA_HOME")
		err = os.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "xdg-data"))
		Expect(err).NotTo(HaveOccurred())

		err = os.Chdir(tempDir)
		Expect(err).NotTo(HaveOccurred())

		fakeClaudeDir := filepath.Join(tempDir, "bin")
		err = os.Mkdir(fakeClaudeDir, 0o755)
		Expect(err).NotTo(HaveOccurred())

		_, _, err = createFakeClaude(fakeClaudeDir)
		Expect(err).NotTo(HaveOccurred())

		clydeRoot = config.GlobalDataDir()
		err = os.MkdirAll(config.GetSessionsDir(clydeRoot), 0o755)
		Expect(err).NotTo(HaveOccurred())

		store = session.NewFileStore(clydeRoot)

		originalLogDir = notify.LogDir
		notifyLogDir = filepath.Join(tempDir, "notify-logs")
		notify.LogDir = notifyLogDir
	})

	AfterEach(func() {
		notify.LogDir = originalLogDir
		if originalXDG == "" {
			_ = os.Unsetenv("XDG_DATA_HOME")
		} else {
			_ = os.Setenv("XDG_DATA_HOME", originalXDG)
		}
		_ = os.Chdir(originalWd)
	})

	Describe("hook sessionstart", func() {
		Context("source: startup", func() {
			It("should handle startup for new sessions without error", func() {
				hookInput := map[string]string{
					"session_id": "some-uuid",
					"source":     "startup",
				}
				inputJSON, err := json.Marshal(hookInput)
				Expect(err).NotTo(HaveOccurred())

				err = executeHookWithInput("sessionstart", inputJSON)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should handle non-clyde project gracefully", func() {
				nonClydeDir := GinkgoT().TempDir()
				err := os.Chdir(nonClydeDir)
				Expect(err).NotTo(HaveOccurred())

				hookInput := map[string]string{
					"session_id": "test-uuid",
					"source":     "startup",
				}
				inputJSON, err := json.Marshal(hookInput)
				Expect(err).NotTo(HaveOccurred())

				err = executeHookWithInput("sessionstart", inputJSON)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should log event to JSONL file", func() {
				hookInput := map[string]string{
					"session_id": "log-test-uuid",
					"source":     "startup",
				}
				inputJSON, err := json.Marshal(hookInput)
				Expect(err).NotTo(HaveOccurred())

				err = executeHookWithInput("sessionstart", inputJSON)
				Expect(err).NotTo(HaveOccurred())

				logFile := filepath.Join(notifyLogDir, "log-test-uuid.events.jsonl")
				Expect(logFile).To(BeAnExistingFile())

				content, err := os.ReadFile(logFile)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(content)).To(ContainSubstring("log-test-uuid"))
				Expect(string(content)).To(ContainSubstring("startup"))
			})

			It("should not error when session has context set", func() {
				sess := session.NewSession("session-with-context", "test-uuid-ctx")
				sess.Metadata.Context = "working on GH-123"
				err := store.Create(sess)
				Expect(err).NotTo(HaveOccurred())

				_ = os.Setenv("CLYDE_SESSION_NAME", "session-with-context")
				defer func() { _ = os.Unsetenv("CLYDE_SESSION_NAME") }()

				hookInput := map[string]string{
					"session_id": "test-uuid-ctx",
					"source":     "startup",
				}
				inputJSON, err := json.Marshal(hookInput)
				Expect(err).NotTo(HaveOccurred())

				err = executeHookWithInput("sessionstart", inputJSON)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should save transcript path from hook input", func() {
				sess := session.NewSession("session-with-transcript", "test-uuid-123")
				err := store.Create(sess)
				Expect(err).NotTo(HaveOccurred())

				_ = os.Setenv("CLYDE_SESSION_NAME", "session-with-transcript")
				defer func() { _ = os.Unsetenv("CLYDE_SESSION_NAME") }()

				hookInput := map[string]string{
					"session_id":      "test-uuid-123",
					"transcript_path": "/home/user/.claude/projects/test-project/test-uuid-123.jsonl",
					"source":          "startup",
				}
				inputJSON, err := json.Marshal(hookInput)
				Expect(err).NotTo(HaveOccurred())

				err = executeHookWithInput("sessionstart", inputJSON)
				Expect(err).NotTo(HaveOccurred())

				updatedSess, err := store.Get("session-with-transcript")
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedSess.Metadata.ProviderTranscriptPath()).To(Equal("/home/user/.claude/projects/test-project/test-uuid-123.jsonl"))
			})
		})

		Context("source: resume", func() {
			It("should handle non-clyde project gracefully", func() {
				nonClydeDir := GinkgoT().TempDir()
				err := os.Chdir(nonClydeDir)
				Expect(err).NotTo(HaveOccurred())

				hookInput := map[string]string{
					"session_id": "resume-uuid",
					"source":     "resume",
				}
				inputJSON, err := json.Marshal(hookInput)
				Expect(err).NotTo(HaveOccurred())

				err = executeHookWithInput("sessionstart", inputJSON)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should work with invalid JSON input gracefully", func() {
				err := executeHookWithInput("sessionstart", []byte("not json"))
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("parse"))
			})

			It("should save transcript path from hook input on resume", func() {
				sess := session.NewSession("session-resume-transcript", "test-uuid-456")
				err := store.Create(sess)
				Expect(err).NotTo(HaveOccurred())

				_ = os.Setenv("CLYDE_SESSION_NAME", "session-resume-transcript")
				defer func() { _ = os.Unsetenv("CLYDE_SESSION_NAME") }()

				hookInput := map[string]string{
					"session_id":      "test-uuid-456",
					"transcript_path": "/home/user/.claude/projects/test-project/test-uuid-456.jsonl",
					"source":          "resume",
				}
				inputJSON, err := json.Marshal(hookInput)
				Expect(err).NotTo(HaveOccurred())

				err = executeHookWithInput("sessionstart", inputJSON)
				Expect(err).NotTo(HaveOccurred())

				updatedSess, err := store.Get("session-resume-transcript")
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedSess.Metadata.ProviderTranscriptPath()).To(Equal("/home/user/.claude/projects/test-project/test-uuid-456.jsonl"))
			})
		})
	})
})
