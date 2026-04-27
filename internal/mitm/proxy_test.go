package mitm

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/config"
)

func TestPrepareCodexOverlayCopiesAuthAndWritesConfig(t *testing.T) {
	sourceHome := t.TempDir()
	authPath := filepath.Join(sourceHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"tok"}}`), 0o600); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	overlay, err := PrepareCodexOverlay(context.Background(), config.MITMConfig{
		EnabledDefault: true,
		Providers:      "codex",
		BodyMode:       "summary",
		CaptureDir:     t.TempDir(),
	}, slog.Default(), sourceHome)
	if err != nil {
		t.Fatalf("PrepareCodexOverlay: %v", err)
	}
	if overlay == nil {
		t.Fatalf("overlay=nil")
	}
	data, err := os.ReadFile(filepath.Join(overlay.Home, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "openai_base_url") || !strings.Contains(text, "chatgpt_base_url") {
		t.Fatalf("config.toml missing proxy base urls: %s", text)
	}
	if _, err := os.Stat(filepath.Join(overlay.Home, "auth.json")); err != nil {
		t.Fatalf("auth.json not copied: %v", err)
	}
}
