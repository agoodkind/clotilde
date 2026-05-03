package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	codexstore "goodkind.io/clyde/internal/providers/codex/store"
	"goodkind.io/clyde/internal/session"
)

func TestLifecycleDeleteArtifactsDeletesKnownCodexRollouts(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	paths, err := codexstore.ResolveStorePaths(codexHome, "")
	if err != nil {
		t.Fatalf("ResolveStorePaths returned error: %v", err)
	}
	currentID := "019de9aa-3a00-7010-bd9f-a6ee71559357"
	previousID := "019de9bb-3a00-7010-bd9f-a6ee71559357"
	currentDir := filepath.Join(paths.SessionsDir, "2026", "05", "02")
	if err := os.MkdirAll(currentDir, 0o755); err != nil {
		t.Fatalf("mkdir current dir: %v", err)
	}
	if err := os.MkdirAll(paths.ArchivedSessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir archived dir: %v", err)
	}
	currentPath := filepath.Join(currentDir, "rollout-2026-05-02T10-09-00-"+currentID+".jsonl")
	previousPath := filepath.Join(paths.ArchivedSessionsDir, "rollout-2026-05-02T09-00-00-"+previousID+".jsonl")
	if err := os.WriteFile(currentPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write current rollout: %v", err)
	}
	if err := os.WriteFile(previousPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write previous rollout: %v", err)
	}
	sess := &session.Session{
		Name: "codex-session",
		Metadata: session.Metadata{
			Name:     "codex-session",
			Provider: session.ProviderCodex,
			ProviderState: &session.ProviderOwnedMetadata{
				Current: session.ProviderSessionID{Provider: session.ProviderCodex, ID: currentID},
				Previous: []session.ProviderSessionID{
					{Provider: session.ProviderCodex, ID: previousID},
				},
			},
		},
	}

	deleted, err := NewLifecycle().DeleteArtifacts(context.Background(), session.DeleteArtifactsRequest{
		Session: sess,
	})
	if err != nil {
		t.Fatalf("DeleteArtifacts returned error: %v", err)
	}
	if len(deleted.Transcripts) != 2 {
		t.Fatalf("deleted transcripts = %v, want two paths", deleted.Transcripts)
	}
	for _, path := range []string{currentPath, previousPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rollout %s still exists or stat failed with %v", path, err)
		}
	}
}
