package artifacts

import (
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/session"
)

func TestDeleteSessionArtifactsDeletesCurrentAndPreviousIDs(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	projectRoot := filepath.Join(t.TempDir(), "project")
	clydeRoot := filepath.Join(projectRoot, config.ClydeDir)
	projectsDir := filepath.Join(homeDir, ".claude", "projects", projectDir(clydeRoot))
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}

	currentTranscript := filepath.Join(projectsDir, "current.jsonl")
	previousTranscript := filepath.Join(projectsDir, "previous.jsonl")
	currentAgentLog := filepath.Join(projectsDir, "agent-current.jsonl")
	previousAgentLog := filepath.Join(projectsDir, "agent-previous.jsonl")
	for path, body := range map[string]string{
		currentTranscript:  "{}\n",
		previousTranscript: "{}\n",
		currentAgentLog:    "current\n",
		previousAgentLog:   "previous\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	sess := &session.Session{
		Name: "chat-1",
		Metadata: session.Metadata{
			Provider:           session.ProviderClaude,
			SessionID:          "current",
			TranscriptPath:     currentTranscript,
			PreviousSessionIDs: []string{"previous"},
		},
	}
	deleted, err := DeleteSessionArtifacts(clydeRoot, sess)
	if err != nil {
		t.Fatalf("DeleteSessionArtifacts returned error: %v", err)
	}
	if len(deleted.Transcript) != 2 {
		t.Fatalf("deleted transcripts = %v, want 2", deleted.Transcript)
	}
	if len(deleted.AgentLogs) != 2 {
		t.Fatalf("deleted agent logs = %v, want 2", deleted.AgentLogs)
	}
	for _, path := range []string{currentTranscript, previousTranscript, currentAgentLog, previousAgentLog} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists or stat failed with %v", path, err)
		}
	}
}
