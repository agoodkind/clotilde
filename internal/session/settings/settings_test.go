package settings

import (
	"strings"
	"testing"

	"goodkind.io/clyde/internal/session"
)

func TestSaveRejectsUnsupportedProvider(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	sess := unsupportedProviderSession()

	err := Save(store, sess, &session.Settings{Model: "codex"})
	if err == nil {
		t.Fatal("Save returned nil error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported session provider") {
		t.Fatalf("Save error = %q, want unsupported provider", err)
	}
}

func TestLoadRejectsUnsupportedProvider(t *testing.T) {
	store := session.NewFileStore(t.TempDir())
	sess := unsupportedProviderSession()

	loaded, err := Load(store, sess)
	if err == nil {
		t.Fatal("Load returned nil error for unsupported provider")
	}
	if loaded != nil {
		t.Fatalf("Load returned settings for unsupported provider: %#v", loaded)
	}
	if !strings.Contains(err.Error(), "unsupported session provider") {
		t.Fatalf("Load error = %q, want unsupported provider", err)
	}
}

func unsupportedProviderSession() *session.Session {
	return &session.Session{
		Name: "codex-like",
		Metadata: session.Metadata{
			Name:     "codex-like",
			Provider: session.ProviderCodex,
			ProviderState: &session.ProviderOwnedMetadata{
				Current: session.ProviderSessionID{Provider: session.ProviderCodex, ID: "provider-id"},
			},
		},
	}
}
