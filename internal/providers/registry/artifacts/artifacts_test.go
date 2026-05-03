package artifacts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestDeleteClaudeProviderDeletesTranscriptAndAgentLogs(t *testing.T) {
	clydeRoot := filepath.Join(t.TempDir(), "project", ".claude", "clyde")
	claudeProjectDir := t.TempDir()
	transcriptPath := filepath.Join(claudeProjectDir, "claude-current.jsonl")
	agentLogPath := filepath.Join(claudeProjectDir, "agent-claude.jsonl")
	unrelatedAgentLogPath := filepath.Join(claudeProjectDir, "agent-unrelated.jsonl")
	writeFile(t, transcriptPath, "{}\n")
	writeFile(t, agentLogPath, "event for claude-current\n")
	writeFile(t, unrelatedAgentLogPath, "event for another-session\n")
	sess := session.NewSession("claude-like", "claude-current")
	sess.Metadata.SetProviderTranscriptPath(transcriptPath)

	deleted, err := Delete(context.Background(), session.DeleteArtifactsRequest{
		Session:   sess,
		ClydeRoot: clydeRoot,
	})
	if err != nil {
		t.Fatalf("Delete returned error for claude provider: %v", err)
	}
	if deleted == nil {
		t.Fatal("Delete returned nil deleted artifacts for claude provider")
	}
	assertStringSet(t, deleted.Transcripts, []string{transcriptPath})
	assertStringSet(t, deleted.AgentLogs, []string{agentLogPath})
	assertMissingPath(t, transcriptPath)
	assertMissingPath(t, agentLogPath)
	assertExistingPath(t, unrelatedAgentLogPath)
}

func TestDeleteCodexProviderDeletesRolloutArtifacts(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "05", "02")
	rolloutPath := filepath.Join(rolloutDir, "rollout-2026-05-02T10-09-00-provider-id.jsonl")
	writeFile(t, rolloutPath, "")
	sess := newProviderSession("codex-like", session.ProviderCodex, "provider-id")

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
	assertStringSet(t, deleted.Transcripts, []string{rolloutPath})
	if len(deleted.AgentLogs) != 0 {
		t.Fatalf("Delete returned agent logs for codex provider: %#v", deleted.AgentLogs)
	}
	assertMissingPath(t, rolloutPath)
}

func TestDeleteRejectsNilSession(t *testing.T) {
	deleted, err := Delete(context.Background(), session.DeleteArtifactsRequest{
		Session:   nil,
		ClydeRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Delete returned nil error for nil session")
	}
	if deleted != nil {
		t.Fatalf("Delete returned deleted artifacts for nil session: %#v", deleted)
	}
	if !strings.Contains(err.Error(), "nil session") {
		t.Fatalf("Delete error = %q, want nil session", err)
	}
}

func TestDeleteRejectsUnsupportedProvider(t *testing.T) {
	sess := newProviderSession("unknown-like", session.ProviderID("other"), "provider-id")

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

func newProviderSession(name string, provider session.ProviderID, providerSessionID string) *session.Session {
	sess := session.NewSession(name, providerSessionID)
	sess.Metadata.Provider = provider
	sess.Metadata.ProviderState = &session.ProviderOwnedMetadata{
		Current: session.ProviderSessionID{Provider: provider, ID: providerSessionID},
	}
	sess.Metadata.NormalizeProviderState()
	return sess
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertStringSet(t *testing.T, actual []string, expected []string) {
	t.Helper()
	if len(actual) != len(expected) {
		t.Fatalf("strings = %#v, want %#v", actual, expected)
	}
	seen := make(map[string]bool, len(actual))
	for _, value := range actual {
		seen[value] = true
	}
	for _, value := range expected {
		if !seen[value] {
			t.Fatalf("strings = %#v, want %#v", actual, expected)
		}
	}
}

func assertMissingPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path %s still exists or stat failed with %v", path, err)
	}
}

func assertExistingPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("path %s does not exist: %v", path, err)
	}
}
