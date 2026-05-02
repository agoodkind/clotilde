package lifecycle

import (
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestForProviderClaude(t *testing.T) {
	runtime, err := ForProvider(session.ProviderClaude, nil)
	if err != nil {
		t.Fatalf("ForProvider returned error: %v", err)
	}
	if runtime == nil {
		t.Fatal("ForProvider returned nil runtime")
	}
}

func TestForProviderUnknown(t *testing.T) {
	runtime, err := ForProvider(session.ProviderID("unknown"), nil)
	if err == nil {
		t.Fatal("ForProvider returned nil error for unknown provider")
	}
	if runtime != nil {
		t.Fatalf("ForProvider returned runtime %#v for unknown provider", runtime)
	}
}

func TestForProviderCodex(t *testing.T) {
	runtime, err := ForProvider(session.ProviderCodex, nil)
	if err != nil {
		t.Fatalf("ForProvider returned error: %v", err)
	}
	if runtime == nil {
		t.Fatal("ForProvider returned nil runtime")
	}

	sess := session.NewSession("codex-session", "codex-123")
	sess.Metadata.Provider = session.ProviderCodex
	sess.Metadata.ProviderState = &session.ProviderOwnedMetadata{
		Current: session.ProviderSessionID{Provider: session.ProviderCodex, ID: "codex-123"},
	}
	lines := runtime.ResumeInstructions(sess)
	if len(lines) != 1 || lines[0] != "codex resume codex-123" {
		t.Fatalf("ResumeInstructions returned %v, want [codex resume codex-123]", lines)
	}
}
