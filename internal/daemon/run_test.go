package daemon

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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

	err := ctrl.apply(adapterLaunchConfig{Enabled: false}, false)
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

	err := ctrl.apply(adapterLaunchConfig{Enabled: false}, false)
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
