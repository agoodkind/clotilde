package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanCodexReturnsRolloutSessionMeta(t *testing.T) {
	codexHome := t.TempDir()
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "05", "02")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	path := filepath.Join(rolloutDir, "rollout-2026-05-02T10-09-00-019de9aa-3a00-7010-bd9f-a6ee71559357.jsonl")
	body := `{"timestamp":"2026-05-02T17:09:04.407Z","type":"session_meta","payload":{"id":"019de9aa-3a00-7010-bd9f-a6ee71559357","timestamp":"2026-05-02T17:09:00.555Z","cwd":"/repo","originator":"codex-tui","cli_version":"0.128.0","source":"cli","model_provider":"openai"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	results, err := newCodexDiscoveryScanner(codexHome).Scan()
	if err != nil {
		t.Fatalf("ScanCodex returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ScanCodex returned %d results, want 1", len(results))
	}
	got := results[0]
	if got.Provider != ProviderCodex {
		t.Fatalf("Provider = %q, want %q", got.Provider, ProviderCodex)
	}
	if got.ProviderSessionID() != "019de9aa-3a00-7010-bd9f-a6ee71559357" {
		t.Fatalf("ProviderSessionID = %q", got.ProviderSessionID())
	}
	if got.WorkspaceRoot != "/repo" {
		t.Fatalf("WorkspaceRoot = %q, want /repo", got.WorkspaceRoot)
	}
	if got.Entrypoint != "codex-tui" {
		t.Fatalf("Entrypoint = %q, want codex-tui", got.Entrypoint)
	}
	if got.PrimaryArtifactPath() != path {
		t.Fatalf("PrimaryArtifactPath = %q, want %q", got.PrimaryArtifactPath(), path)
	}
	if got.FirstEntryTime.IsZero() {
		t.Fatal("FirstEntryTime is zero")
	}
}

func TestScanCodexMarksThreadSpawnSubagents(t *testing.T) {
	codexHome := t.TempDir()
	rolloutDir := filepath.Join(codexHome, "sessions", "2026", "05", "02")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	path := filepath.Join(rolloutDir, "rollout-2026-05-02T10-09-00-019de9aa-3a00-7010-bd9f-a6ee71559357.jsonl")
	body := `{"timestamp":"2026-05-02T17:09:04.407Z","type":"session_meta","payload":{"id":"019de9aa-3a00-7010-bd9f-a6ee71559357","timestamp":"2026-05-02T17:09:00.555Z","cwd":"/repo","originator":"codex-tui","cli_version":"0.128.0","source":{"subagent":{"thread_spawn":{"parent_thread_id":"019de912-5c55-70c3-b9d6-9bc5d3860cde","depth":1,"agent_path":null,"agent_nickname":"Mencius","agent_role":"worker"}}},"agent_nickname":"Mencius","agent_role":"worker","model_provider":"openai"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	results, err := newCodexDiscoveryScanner(codexHome).Scan()
	if err != nil {
		t.Fatalf("ScanCodex returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("ScanCodex returned %d results, want 1", len(results))
	}
	got := results[0]
	if !got.IsSubagent {
		t.Fatal("IsSubagent = false, want true")
	}
	if !got.IsForked {
		t.Fatal("IsForked = false, want true")
	}
	if got.ForkParent.ID != "019de912-5c55-70c3-b9d6-9bc5d3860cde" {
		t.Fatalf("ForkParent.ID = %q", got.ForkParent.ID)
	}
}
