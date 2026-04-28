package mitm

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"goodkind.io/clyde/internal/config"
)

func TestLaunchHintsForClaudeCodeReturnsEnvOnly(t *testing.T) {
	cfg := config.MITMConfig{EnabledDefault: true, Providers: "claude"}
	hints, err := LaunchHintsFor(context.Background(), cfg, "claude-code", silent())
	if err != nil {
		t.Fatalf("hints: %v", err)
	}
	if hints.Env["ANTHROPIC_BASE_URL"] == "" {
		t.Errorf("expected ANTHROPIC_BASE_URL in env, got %v", hints.Env)
	}
	if len(hints.Args) != 0 {
		t.Errorf("CLI should have no chromium args, got %v", hints.Args)
	}
}

func TestLaunchHintsForClaudeDesktopReturnsChromiumFlags(t *testing.T) {
	cfg := config.MITMConfig{EnabledDefault: true, Providers: "claude"}
	hints, err := LaunchHintsFor(context.Background(), cfg, "claude-desktop", silent())
	if err != nil {
		t.Fatalf("hints: %v", err)
	}
	if hints.Env["ANTHROPIC_BASE_URL"] == "" {
		t.Errorf("expected ANTHROPIC_BASE_URL in env even for desktop, got %v", hints.Env)
	}
	if len(hints.Args) == 0 {
		t.Errorf("expected chromium flags for desktop, got %v", hints.Args)
	}
}

func TestLaunchHintsForReturnsZeroWhenDisabled(t *testing.T) {
	cfg := config.MITMConfig{EnabledDefault: false}
	hints, err := LaunchHintsFor(context.Background(), cfg, "claude-code", silent())
	if err != nil {
		t.Fatalf("hints: %v", err)
	}
	if len(hints.Env) != 0 || len(hints.Args) != 0 {
		t.Errorf("expected zero hints when disabled, got %+v", hints)
	}
}

func TestLaunchHintsForReturnsZeroWhenProviderNotListed(t *testing.T) {
	cfg := config.MITMConfig{EnabledDefault: true, Providers: "codex"}
	hints, err := LaunchHintsFor(context.Background(), cfg, "claude-code", silent())
	if err != nil {
		t.Fatalf("hints: %v", err)
	}
	if len(hints.Env) != 0 || len(hints.Args) != 0 {
		t.Errorf("expected zero hints when provider not listed, got %+v", hints)
	}
}

func silent() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
