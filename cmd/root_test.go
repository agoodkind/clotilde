package cmd

import (
	"os"
	"testing"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/ui"
)

func TestSessionEventFromProtoMapsBinaryUpdate(t *testing.T) {
	ev := sessionEventFromProto(&clydev1.SubscribeRegistryResponse{
		Kind:         clydev1.SubscribeRegistryResponse_KIND_CLYDE_BINARY_UPDATED,
		BinaryPath:   "/tmp/clyde",
		BinaryReason: "mtime_changed",
		BinaryHash:   "abc123",
	})

	if ev.Kind != "CLYDE_BINARY_UPDATED" {
		t.Fatalf("kind=%q want CLYDE_BINARY_UPDATED", ev.Kind)
	}
	if ev.BinaryPath != "/tmp/clyde" {
		t.Fatalf("binary path=%q want /tmp/clyde", ev.BinaryPath)
	}
	if ev.BinaryReason != "mtime_changed" {
		t.Fatalf("binary reason=%q want mtime_changed", ev.BinaryReason)
	}
	if ev.BinaryHash != "abc123" {
		t.Fatalf("binary hash=%q want abc123", ev.BinaryHash)
	}
}

func TestConsumeTUIReturnSessionRestoresAndClearsEnv(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store := session.NewFileStoreReadOnly(config.GlobalDataDir())
	want := session.NewSession("chat-one", "session-uuid")
	if err := store.Create(want); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Setenv(ui.EnvTUIReturnSessionID, "session-uuid")
	t.Setenv(ui.EnvTUIReturnSessionName, "chat-one")

	got := consumeTUIReturnSession()

	if got == nil {
		t.Fatalf("consumeTUIReturnSession returned nil")
	}
	if got.Name != "chat-one" || got.Metadata.SessionID != "session-uuid" {
		t.Fatalf("restored session = %s/%s, want chat-one/session-uuid", got.Name, got.Metadata.SessionID)
	}
	if value := os.Getenv(ui.EnvTUIReturnSessionID); value != "" {
		t.Fatalf("%s still set to %q", ui.EnvTUIReturnSessionID, value)
	}
	if value := os.Getenv(ui.EnvTUIReturnSessionName); value != "" {
		t.Fatalf("%s still set to %q", ui.EnvTUIReturnSessionName, value)
	}
}
