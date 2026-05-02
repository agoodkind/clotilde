package slogger

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/config"
)

func TestSetupWritesConcernLogToHardCodedNestedTree(t *testing.T) {
	root := t.TempDir()
	unified := filepath.Join(root, "clyde-daemon.jsonl")
	t.Setenv(envOverride, unified)

	closer, err := Setup(config.LoggingConfig{
		Level: "debug",
		Rotation: config.LoggingRotation{
			Enabled: new(false),
		},
	}, ProcessRoleDaemon)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	For(ConcernAdapterModelsCatalog).Info("adapter.models.listed", "model_count", 42)
	slog.Info("unconcerned.event")
	_ = closer.Close()

	modelsPath := filepath.Join(root, "logs", "adapter", "models", "catalog.jsonl")
	modelsLog, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatalf("read concern log %s: %v", modelsPath, err)
	}
	if !strings.Contains(string(modelsLog), `"msg":"adapter.models.listed"`) {
		t.Fatalf("concern log missing models event: %s", modelsLog)
	}
	if strings.Contains(string(modelsLog), "unconcerned.event") {
		t.Fatalf("concern log should not include unconcerned event: %s", modelsLog)
	}

	unifiedLog, err := os.ReadFile(unified)
	if err != nil {
		t.Fatalf("read unified log: %v", err)
	}
	if !strings.Contains(string(unifiedLog), "adapter.models.listed") || !strings.Contains(string(unifiedLog), "unconcerned.event") {
		t.Fatalf("unified log should keep both events: %s", unifiedLog)
	}
}

func TestSetupRoutesExistingEventNamesWhenConcernIsExplicit(t *testing.T) {
	root := t.TempDir()
	unified := filepath.Join(root, "clyde-daemon.jsonl")
	t.Setenv(envOverride, unified)

	closer, err := Setup(config.LoggingConfig{
		Level: "debug",
		Rotation: config.LoggingRotation{
			Enabled: new(false),
		},
	}, ProcessRoleDaemon)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	For(ConcernSessionDiscoveryScan).Warn("session.scan.walk_failed", "path", "/tmp/nope")
	For(ConcernDaemonRPCRequests).Info("daemon.rpc.started", "method", "/clyde.v1.Daemon/ListSessions")
	For(ConcernUITUIActions).Debug("tui.input.key", "key", "enter")
	_ = closer.Close()

	assertLogContains(t, filepath.Join(root, "logs", "session", "discovery", "scan.jsonl"), "session.scan.walk_failed")
	assertLogContains(t, filepath.Join(root, "logs", "daemon", "rpc", "requests.jsonl"), "daemon.rpc.started")
	assertLogContains(t, filepath.Join(root, "logs", "ui", "tui", "actions.jsonl"), "tui.input.key")
}

func TestConcernLoggerResolvesDefaultAfterSetup(t *testing.T) {
	root := t.TempDir()
	unified := filepath.Join(root, "clyde-daemon.jsonl")
	t.Setenv(envOverride, unified)

	early := Concern(ConcernSessionDomainResolve)

	closer, err := Setup(config.LoggingConfig{
		Level: "debug",
		Rotation: config.LoggingRotation{
			Enabled: new(false),
		},
	}, ProcessRoleDaemon)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	early.Logger().Info("session.resolve.lazy_logger", "session", "demo")
	_ = closer.Close()

	unifiedLog, err := os.ReadFile(unified)
	if err != nil {
		t.Fatalf("read unified log: %v", err)
	}
	if strings.Contains(string(unifiedLog), `"msg":"INFO session.resolve.lazy_logger`) {
		t.Fatalf("lazy concern logger used bootstrap text logger: %s", unifiedLog)
	}
	if !strings.Contains(string(unifiedLog), `"msg":"session.resolve.lazy_logger"`) {
		t.Fatalf("unified log missing lazy logger event: %s", unifiedLog)
	}
	assertLogContains(t, filepath.Join(root, "logs", "session", "domain", "resolve.jsonl"), "session.resolve.lazy_logger")
}

func TestConcernForEventCoversPrimaryTree(t *testing.T) {
	cases := map[string]string{
		"adapter.codex.transport.prepared": ConcernAdapterProviderCodexWS,
		"adapter.anthropic.ingress":        ConcernAdapterProviderAnthReq,
		"session.adopt.completed":          ConcernSessionDiscoveryAdopt,
		"session.resolve.tier1_hit":        ConcernSessionDomainResolve,
		"prune.autoname.started":           ConcernDaemonWorkersPrune,
		"mitm.ws.closed":                   ConcernProviderMITMWire,
		"compact.apply.completed":          ConcernCompactApply,
		"mcp.context.loaded":               ConcernMCPServerContext,
	}
	for event, want := range cases {
		if got := concernForEvent(event); got != want {
			t.Fatalf("concernForEvent(%q)=%q want %q", event, got, want)
		}
	}
}

func assertLogContains(t *testing.T, path string, needle string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log %s: %v", path, err)
	}
	if !strings.Contains(string(content), needle) {
		t.Fatalf("log %s missing %q: %s", path, needle, content)
	}
}
