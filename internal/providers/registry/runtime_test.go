package registry

import (
	"strings"
	"testing"

	claudelifecycle "goodkind.io/clyde/internal/providers/claude/lifecycle"
	codexlifecycle "goodkind.io/clyde/internal/providers/codex/lifecycle"
	"goodkind.io/clyde/internal/session"
)

func TestDefaultReturnsClaudeRuntime(t *testing.T) {
	runtime, err := Default(nil)
	if err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	assertClaudeRuntime(t, runtime)
}

func TestForProviderReturnsClaudeRuntimeForDefaultProvider(t *testing.T) {
	runtime, err := ForProvider(session.ProviderClaude, nil)
	if err != nil {
		t.Fatalf("ForProvider returned error: %v", err)
	}
	assertClaudeRuntime(t, runtime)
}

func TestForProviderReturnsClaudeRuntimeForLegacyProvider(t *testing.T) {
	runtime, err := ForProvider(session.ProviderUnknown, nil)
	if err != nil {
		t.Fatalf("ForProvider returned error for legacy provider: %v", err)
	}
	assertClaudeRuntime(t, runtime)
}

func TestForProviderReturnsCodexRuntime(t *testing.T) {
	runtime, err := ForProvider(session.ProviderCodex, nil)
	if err != nil {
		t.Fatalf("ForProvider returned error: %v", err)
	}
	assertCodexRuntime(t, runtime)
}

func TestForProviderRejectsUnknownProvider(t *testing.T) {
	runtime, err := ForProvider(session.ProviderID("unknown"), nil)
	if err == nil {
		t.Fatal("ForProvider returned nil error for unknown provider")
	}
	if runtime != nil {
		t.Fatalf("ForProvider returned runtime %#v for unknown provider", runtime)
	}
	if !strings.Contains(err.Error(), "unsupported session provider") {
		t.Fatalf("ForProvider error = %q, want unsupported provider", err)
	}
}

func TestForSessionReturnsClaudeRuntimeForDefaultSession(t *testing.T) {
	sess := session.NewSession("claude-session", "claude-123")

	runtime, err := ForSession(sess, nil)
	if err != nil {
		t.Fatalf("ForSession returned error: %v", err)
	}
	assertClaudeRuntime(t, runtime)
}

func TestForSessionReturnsCodexRuntimeForCodexSession(t *testing.T) {
	sess := newCodexSession("codex-session", "codex-123")

	runtime, err := ForSession(sess, nil)
	if err != nil {
		t.Fatalf("ForSession returned error: %v", err)
	}
	assertCodexRuntime(t, runtime)

	instructions := runtime.ResumeInstructions(sess)
	if len(instructions) != 1 || instructions[0] != "codex resume codex-123" {
		t.Fatalf("ResumeInstructions returned %v, want [codex resume codex-123]", instructions)
	}
}

func TestForSessionRejectsNilSession(t *testing.T) {
	runtime, err := ForSession(nil, nil)
	if err == nil {
		t.Fatal("ForSession returned nil error for nil session")
	}
	if runtime != nil {
		t.Fatalf("ForSession returned runtime %#v for nil session", runtime)
	}
	if !strings.Contains(err.Error(), "nil session") {
		t.Fatalf("ForSession error = %q, want nil session", err)
	}
}

func assertClaudeRuntime(t *testing.T, runtime Runtime) {
	t.Helper()
	if runtime == nil {
		t.Fatal("runtime is nil")
	}
	if _, ok := runtime.(*claudelifecycle.Lifecycle); !ok {
		t.Fatalf("runtime type = %T, want *claude.Lifecycle", runtime)
	}
}

func assertCodexRuntime(t *testing.T, runtime Runtime) {
	t.Helper()
	if runtime == nil {
		t.Fatal("runtime is nil")
	}
	if _, ok := runtime.(*codexlifecycle.Lifecycle); !ok {
		t.Fatalf("runtime type = %T, want *codex.Lifecycle", runtime)
	}
}

func newCodexSession(name string, providerSessionID string) *session.Session {
	sess := session.NewSession(name, providerSessionID)
	sess.Metadata.Provider = session.ProviderCodex
	sess.Metadata.ProviderState = &session.ProviderOwnedMetadata{
		Current: session.ProviderSessionID{Provider: session.ProviderCodex, ID: providerSessionID},
	}
	sess.Metadata.NormalizeProviderState()
	return sess
}
