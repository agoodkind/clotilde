package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/config"
)

func TestIsAdapterConfigEvent(t *testing.T) {
	cfgDir := t.TempDir()
	tomlPath := filepath.Join(cfgDir, "config.toml")
	jsonPath := filepath.Join(cfgDir, "config.json")

	if !isAdapterConfigEvent(fsnotify.Event{Name: tomlPath, Op: fsnotify.Write}, tomlPath, jsonPath) {
		t.Fatalf("toml write should trigger reload")
	}
	if !isAdapterConfigEvent(fsnotify.Event{Name: jsonPath, Op: fsnotify.Create}, tomlPath, jsonPath) {
		t.Fatalf("json create should trigger reload")
	}
	if isAdapterConfigEvent(fsnotify.Event{Name: filepath.Join(cfgDir, "notes.txt"), Op: fsnotify.Write}, tomlPath, jsonPath) {
		t.Fatalf("unrelated file should not trigger reload")
	}
	if isAdapterConfigEvent(fsnotify.Event{Name: tomlPath, Op: fsnotify.Op(0)}, tomlPath, jsonPath) {
		t.Fatalf("non-mutating event should not trigger reload")
	}
}

func TestAdapterControllerApplyNoopDoesNotStopProcess(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stopped := false
	proc := &adapterProcess{
		cancel: func() { stopped = true },
		done:   make(chan struct{}),
	}
	close(proc.done)

	ctrl := &adapterController{
		log: log,
		current: adapterLaunchConfig{
			Enabled: false,
		},
		proc: proc,
	}

	err := ctrl.apply(adapterLaunchConfig{Enabled: false}, false, nil)
	if err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if stopped {
		t.Fatalf("no-op apply should not stop existing process")
	}
}

func TestAdapterControllerApplyDisableStopsProcess(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	stopped := false
	proc := &adapterProcess{
		cancel: func() { stopped = true },
		done:   make(chan struct{}),
	}
	close(proc.done)

	ctrl := &adapterController{
		log: log,
		current: adapterLaunchConfig{
			Enabled: true,
			Adapter: config.AdapterConfig{Port: 11434},
		},
		proc: proc,
	}

	err := ctrl.apply(adapterLaunchConfig{Enabled: false}, false, nil)
	if err != nil {
		t.Fatalf("apply returned error: %v", err)
	}
	if !stopped {
		t.Fatalf("disable apply should stop existing process")
	}
	if ctrl.proc != nil {
		t.Fatalf("process should be cleared after disable")
	}
	if ctrl.current.Enabled {
		t.Fatalf("current config should be disabled")
	}
}

func TestStopAdapterProcessWaitsForDone(t *testing.T) {
	done := make(chan struct{})
	canceled := false
	proc := &adapterProcess{
		cancel: func() {
			canceled = true
			close(done)
		},
		done: done,
	}
	stopAdapterProcess(proc, 100*time.Millisecond)
	if !canceled {
		t.Fatalf("expected cancel to be called")
	}
}

func TestReloadDaemonCallsReloadFunc(t *testing.T) {
	called := false
	srv := &Server{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		sessions:       map[string]*wrapperSession{"wrapper-reload-1": {wrapperID: "wrapper-reload-1", sessionName: "chat-1"}},
		globalSettings: map[string]any{},
	}
	srv.SetReloadFunc(func(_ context.Context) (reloadReport, error) {
		called = true
		return reloadReport{BinaryReloaded: true, NewPID: 1234}, nil
	})

	resp, err := srv.ReloadDaemon(context.Background(), &clydev1.ReloadDaemonRequest{})
	if err != nil {
		t.Fatalf("reload daemon: %v", err)
	}
	if !called {
		t.Fatalf("reload func was not called")
	}
	if resp.GetActiveSessions() != 1 {
		t.Fatalf("active sessions=%d want 1", resp.GetActiveSessions())
	}
	if !resp.GetBinaryReloaded() {
		t.Fatalf("binary reload flag was not propagated")
	}
	if resp.GetNewPid() != 1234 {
		t.Fatalf("new pid=%d want 1234", resp.GetNewPid())
	}
}

func TestInheritedListenerFilesIncludesDaemonAdapterAndWebapp(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "clyde-lis-")
	if err != nil {
		t.Fatalf("temp socket dir: %v", err)
	}
	defer os.RemoveAll(socketDir)
	daemonLis, err := net.Listen("unix", filepath.Join(socketDir, "d.sock"))
	if err != nil {
		t.Fatalf("listen daemon: %v", err)
	}
	defer daemonLis.Close()
	adapterLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen adapter: %v", err)
	}
	defer adapterLis.Close()
	webLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen webapp: %v", err)
	}
	defer webLis.Close()

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "clyde")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	_, adapterPort, err := net.SplitHostPort(adapterLis.Addr().String())
	if err != nil {
		t.Fatalf("split adapter addr: %v", err)
	}
	_, webPort, err := net.SplitHostPort(webLis.Addr().String())
	if err != nil {
		t.Fatalf("split web addr: %v", err)
	}
	toml := "[adapter]\n" +
		"enabled = true\n" +
		"host = \"127.0.0.1\"\n" +
		"port = " + adapterPort + "\n" +
		"[web_app]\n" +
		"enabled = true\n" +
		"host = \"127.0.0.1\"\n" +
		"port = " + webPort + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	rt := &daemonRuntime{
		listener: daemonLis,
		adapter:  &adapterController{proc: &adapterProcess{lis: adapterLis}},
		webapp:   &webAppProcess{lis: webLis},
	}
	files, specs, cleanup, err := inheritedListenerFiles(rt)
	if err != nil {
		t.Fatalf("inherited files: %v", err)
	}
	defer cleanup()
	if len(files) != 3 || len(specs) != 3 {
		t.Fatalf("got %d files and %d specs, want 3 each", len(files), len(specs))
	}
	gotNames := []string{specs[0].Name, specs[1].Name, specs[2].Name}
	if strings.Join(gotNames, ",") != "daemon,adapter,webapp" {
		t.Fatalf("listener names = %v", gotNames)
	}
	for i, spec := range specs {
		if spec.FD != 3+i {
			t.Fatalf("spec %s fd=%d want %d", spec.Name, spec.FD, 3+i)
		}
	}
}
