package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/config"
)

func TestRunDriftTickSkipsUpstreamWithoutReference(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dcfg := config.MITMDriftConfig{
		Enabled:     true,
		DriftLogDir: t.TempDir(),
		Upstreams: map[string]config.MITMDriftUpstreamCfg{
			"claude-code": {Reference: ""},
		},
	}
	runDriftTick(context.Background(), log, dcfg, []string{"claude-code"})
	out := buf.String()
	if !strings.Contains(out, "mitm.drift.upstream_skipped_no_reference") {
		t.Errorf("expected skip event, got: %s", out)
	}
	if strings.Contains(out, "tick_clean") || strings.Contains(out, "tick_failed") {
		t.Errorf("expected no actual run for empty reference, got: %s", out)
	}
}

func TestDefaultDriftLogDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	want := "/tmp/xdg-state/clyde/mitm-drift"
	if got := defaultDriftLogDir(); got != want {
		t.Errorf("defaultDriftLogDir XDG path: got %q want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "")
	got := defaultDriftLogDir()
	if !strings.HasSuffix(got, ".local/state/clyde/mitm-drift") {
		t.Errorf("defaultDriftLogDir HOME path: got %q", got)
	}
}
