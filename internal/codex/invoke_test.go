package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestLifecycleResumeInteractiveInvokesCodexResume(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record.txt")
	bin := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nprintf 'args=%s\\n' \"$*\" > " + shellQuote(record) + "\nprintf 'cwd=%s\\n' \"$PWD\" >> " + shellQuote(record) + "\nprintf 'session=%s\\n' \"$CLYDE_SESSION_NAME\" >> " + shellQuote(record) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	old := BinaryPathFunc
	BinaryPathFunc = func() string { return bin }
	defer func() { BinaryPathFunc = old }()

	workDir := filepath.Join(dir, "work")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	sess := session.NewSession("codex-session", "codex-123")
	sess.Metadata.Provider = session.ProviderCodex
	sess.Metadata.ProviderState = &session.ProviderOwnedMetadata{
		Current: session.ProviderSessionID{Provider: session.ProviderCodex, ID: "codex-123"},
	}

	err := NewLifecycle().ResumeInteractive(context.Background(), session.ResumeRequest{
		Session: sess,
		Options: session.ResumeOptions{
			CurrentWorkDir: workDir,
		},
	})
	if err != nil {
		t.Fatalf("ResumeInteractive returned error: %v", err)
	}
	gotBytes, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	got := string(gotBytes)
	wd, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatalf("eval work dir: %v", err)
	}
	for _, want := range []string{
		"args=resume codex-123",
		"cwd=" + wd,
		"session=codex-session",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("record missing %q:\n%s", want, got)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
