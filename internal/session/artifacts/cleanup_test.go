package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestDeleteCodexProviderDeletesRolloutArtifacts(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "05", "02")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-05-02T10-09-00-provider-id.jsonl")
	if err := os.WriteFile(rolloutPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
	sess := &session.Session{
		Name: "codex-like",
		Metadata: session.Metadata{
			Name:     "codex-like",
			Provider: session.ProviderCodex,
			ProviderState: &session.ProviderOwnedMetadata{
				Current: session.ProviderSessionID{Provider: session.ProviderCodex, ID: "provider-id"},
			},
		},
	}

	deleted, err := Delete(context.Background(), session.DeleteArtifactsRequest{
		Session:   sess,
		ClydeRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Delete returned error for codex provider: %v", err)
	}
	if deleted == nil {
		t.Fatal("Delete returned nil deleted artifacts for codex provider")
	}
	if len(deleted.Transcripts) != 1 || deleted.Transcripts[0] != rolloutPath {
		t.Fatalf("Delete returned transcripts %#v, want [%s]", deleted.Transcripts, rolloutPath)
	}
	if len(deleted.AgentLogs) != 0 {
		t.Fatalf("Delete returned agent logs for codex provider: %#v", deleted.AgentLogs)
	}
	if _, err := os.Stat(rolloutPath); !os.IsNotExist(err) {
		t.Fatalf("rollout still exists or stat failed with %v", err)
	}
}

func TestDeleteRejectsUnsupportedProvider(t *testing.T) {
	sess := &session.Session{
		Name: "unknown-like",
		Metadata: session.Metadata{
			Name:     "unknown-like",
			Provider: session.ProviderID("other"),
			ProviderState: &session.ProviderOwnedMetadata{
				Current: session.ProviderSessionID{Provider: session.ProviderID("other"), ID: "provider-id"},
			},
		},
	}

	deleted, err := Delete(context.Background(), session.DeleteArtifactsRequest{
		Session:   sess,
		ClydeRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Delete returned nil error for unsupported provider")
	}
	if deleted != nil {
		t.Fatalf("Delete returned deleted artifacts for unsupported provider: %#v", deleted)
	}
	if !strings.Contains(err.Error(), "unsupported session provider") {
		t.Fatalf("Delete error = %q, want unsupported provider", err)
	}
}
