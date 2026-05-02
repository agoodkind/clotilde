package artifacts

import (
	"context"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestDeleteCodexProviderIsNoop(t *testing.T) {
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
	if len(deleted.Transcripts) != 0 || len(deleted.AgentLogs) != 0 {
		t.Fatalf("Delete returned artifacts for codex provider: %#v", deleted)
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
