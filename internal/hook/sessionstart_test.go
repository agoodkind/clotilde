package hook

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestProcessSessionStartAutoAdoptUsesLaunchCWD(t *testing.T) {
	t.Setenv("CLYDE_SESSION_NAME", "chat-test")

	store := session.NewFileStore(t.TempDir())
	launchCWD := filepath.Join(t.TempDir(), "repo", "nested")
	projectRoot := filepath.Dir(launchCWD)
	t.Setenv("CLYDE_LAUNCH_CWD", launchCWD)

	input := bytes.NewBufferString(`{"session_id":"test-uuid","source":"startup","transcript_path":"/tmp/test-uuid.jsonl"}`)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := ProcessSessionStart(context.Background(), store, SessionStartConfig{
		Getwd: func() (string, error) {
			return projectRoot, nil
		},
		FindProjectRoot: func() (string, error) {
			return projectRoot, nil
		},
		LogRawEvent: func([]byte, string) error {
			return nil
		},
	}, log, input, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("ProcessSessionStart: %v", err)
	}

	sess, err := store.Get("chat-test")
	if err != nil {
		t.Fatalf("Get adopted session: %v", err)
	}
	if sess.Metadata.WorkDir != launchCWD {
		t.Fatalf("WorkDir = %q, want %q", sess.Metadata.WorkDir, launchCWD)
	}
	if sess.Metadata.WorkspaceRoot != launchCWD {
		t.Fatalf("WorkspaceRoot = %q, want %q", sess.Metadata.WorkspaceRoot, launchCWD)
	}
}
