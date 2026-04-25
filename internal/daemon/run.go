package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/adapter"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/webapp"
)

// ExtraLoop is an optional background goroutine the daemon owner can
// inject into Run. The loop receives the daemon logger and returns a
// cancel func (or nil if the loop chose not to run). The cancel is
// deferred so the loop shuts down with the daemon. Callers wire
// loops that depend on packages outside the daemon's import graph
// (prune, oauth refresh) without dragging them into the daemon
// package itself.
type ExtraLoop func(log *slog.Logger) func()

const adapterConfigReloadDebounce = 250 * time.Millisecond
const adapterShutdownWait = 4 * time.Second

type adapterLaunchConfig struct {
	Enabled bool
	Adapter config.AdapterConfig
	Logging config.LoggingConfig
}

type adapterProcess struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type adapterController struct {
	log     *slog.Logger
	deps    adapter.Deps
	mu      sync.Mutex
	current adapterLaunchConfig
	proc    *adapterProcess
}

// Run starts the daemon gRPC server on the XDG runtime Unix socket
// and, when the user enables it, the OpenAI compatible HTTP adapter
// on a local port. A single launchd entry boots both layers so the
// monolith stays one process. Additional opt-in background loops
// (prune, oauth refresh) are passed in by the caller.
func Run(log *slog.Logger, extraLoops ...ExtraLoop) error {
	if err := config.EnsureRuntimeDir(); err != nil {
		return err
	}

	lockPath := filepath.Join(config.RuntimeDir(), "daemon.process.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon process lock: %w", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lockFile.Close()
		log.Info("daemon.already_running",
			"component", "daemon",
			"lock_path", lockPath)
		return nil
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

	socketPath := config.DaemonSocketPath()

	// Remove stale socket from a previous run.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", socketPath, err)
	}

	srv, err := New(log)
	if err != nil {
		return fmt.Errorf("failed to create daemon server: %w", err)
	}
	defer srv.Close()

	grpcServer := grpc.NewServer()
	clydev1.RegisterClydeServiceServer(grpcServer, srv)

	adapterCancel, err := startAdapter(log, srv)
	if err != nil {
		return fmt.Errorf("adapter startup: %w", err)
	}
	if adapterCancel != nil {
		defer adapterCancel()
	}

	webCancel := startWebApp(log, srv)
	if webCancel != nil {
		defer webCancel()
	}

	for _, loop := range extraLoops {
		if loop == nil {
			continue
		}
		if cancel := loop(log); cancel != nil {
			defer cancel()
		}
	}

	log.Info("daemon.listening",
		"component", "daemon",
		"socket", socketPath,
	)
	return grpcServer.Serve(listener)
}

// startWebApp boots the optional remote dashboard. The webapp shares
// the daemon process so a single launchd entry covers gRPC, the
// OpenAI adapter, and the dashboard.
func startWebApp(log *slog.Logger, srv *Server) func() {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		log.Warn("webapp.config_load_failed",
			"component", "webapp",
			slog.Any("err", err),
		)
		return nil
	}
	if !cfg.WebApp.Enabled {
		return nil
	}
	deps := webapp.Deps{
		Bridges: func() []webapp.Bridge {
			snap := srv.snapshotBridges()
			out := make([]webapp.Bridge, 0, len(snap))
			for _, b := range snap {
				out = append(out, webapp.Bridge{
					SessionID:       b.SessionId,
					BridgeSessionID: b.BridgeSessionId,
					URL:             b.Url,
					PID:             b.Pid,
				})
			}
			return out
		},
	}
	srvW := webapp.New(cfg.WebApp, deps, log)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := srvW.Start(ctx); err != nil {
			log.Error("webapp.exited",
				"component", "webapp",
				slog.Any("err", err),
			)
		}
	}()
	return cancel
}

// startAdapter reads the global config and, if the adapter is
// enabled, launches the HTTP server in a goroutine. A cancel func
// is returned so Run can shut the listener down on exit. Returns
// (nil, nil) when the adapter is disabled.
//
// Returns an error when the adapter is enabled but
// adapter.New rejects the config (missing families, default model,
// or required client_identity fields). The daemon then exits non-zero so
// launchd reports the failure instead of silently running without
// the OpenAI surface the user asked for.
func startAdapter(log *slog.Logger, srv *Server) (func(), error) {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		log.Warn("adapter.config_load_failed",
			"component", "adapter",
			slog.Any("err", err),
		)
		return nil, nil
	}
	if !cfg.Adapter.Enabled {
		return nil, nil
	}

	ctrl := &adapterController{
		log: log,
		deps: adapter.Deps{
			ResolveClaude: findRealClaude,
			ScratchDir:    adapterScratchDir,
			RequestEvents: srv.providerStats.Record,
		},
	}
	if err := ctrl.apply(launchConfigFromGlobal(cfg), true); err != nil {
		return nil, err
	}

	stopWatch, err := watchAdapterConfig(log, ctrl)
	if err != nil {
		log.Warn("adapter.config_watch_failed",
			"component", "adapter",
			slog.Any("err", err),
		)
	}
	return func() {
		if stopWatch != nil {
			stopWatch()
		}
		ctrl.stop()
	}, nil
}

func launchConfigFromGlobal(cfg *config.Config) adapterLaunchConfig {
	if cfg == nil {
		return adapterLaunchConfig{}
	}
	return adapterLaunchConfig{
		Enabled: cfg.Adapter.Enabled,
		Adapter: cfg.Adapter,
		Logging: cfg.Logging,
	}
}

func watchAdapterConfig(log *slog.Logger, ctrl *adapterController) (func(), error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	configDir := filepath.Dir(config.GlobalConfigPath())
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	if err := watcher.Add(configDir); err != nil {
		_ = watcher.Close()
		return nil, fmt.Errorf("watch config dir: %w", err)
	}
	tomlPath := filepath.Clean(filepath.Join(configDir, "config.toml"))
	jsonPath := filepath.Clean(filepath.Join(configDir, "config.json"))
	log.Info("adapter.config_watch.started",
		"component", "adapter",
		"dir", configDir,
		"toml_path", tomlPath,
		"json_path", jsonPath,
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var timer *time.Timer
		var timerCh <-chan time.Time
		trigger := func() {
			cfg, err := config.LoadGlobalOrDefault()
			if err != nil {
				log.Warn("adapter.config_reload_failed",
					"component", "adapter",
					slog.Any("err", err),
				)
				return
			}
			if err := ctrl.apply(launchConfigFromGlobal(cfg), false); err != nil {
				log.Warn("adapter.config_reload_apply_failed",
					"component", "adapter",
					slog.Any("err", err),
				)
			}
		}

		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if !isAdapterConfigEvent(event, tomlPath, jsonPath) {
					continue
				}
				log.Debug("adapter.config_watch.event",
					"component", "adapter",
					"name", event.Name,
					"op", event.Op.String(),
				)
				if timer == nil {
					timer = time.NewTimer(adapterConfigReloadDebounce)
					timerCh = timer.C
					continue
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(adapterConfigReloadDebounce)
			case <-timerCh:
				timerCh = nil
				timer = nil
				trigger()
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Warn("adapter.config_watch.error",
					"component", "adapter",
					slog.Any("err", err),
				)
			}
		}
	}()

	return func() {
		cancel()
		_ = watcher.Close()
		<-done
	}, nil
}

func isAdapterConfigEvent(event fsnotify.Event, tomlPath, jsonPath string) bool {
	name := filepath.Clean(event.Name)
	if name != tomlPath && name != jsonPath {
		return false
	}
	return event.Has(fsnotify.Write) ||
		event.Has(fsnotify.Create) ||
		event.Has(fsnotify.Remove) ||
		event.Has(fsnotify.Rename) ||
		event.Has(fsnotify.Chmod)
}

func (c *adapterController) apply(next adapterLaunchConfig, startup bool) error {
	c.mu.Lock()
	prev := c.current
	if !startup && reflect.DeepEqual(prev, next) {
		c.mu.Unlock()
		c.log.Debug("adapter.config_reload.noop",
			"component", "adapter",
		)
		return nil
	}
	old := c.proc
	c.mu.Unlock()

	var srv *adapter.Server
	var err error
	if next.Enabled {
		srv, err = adapter.New(next.Adapter, next.Logging, c.deps, c.log)
		if err != nil {
			c.log.Error("adapter.registry.invalid_config",
				"component", "adapter",
				slog.Any("err", err),
			)
			if startup {
				return fmt.Errorf("adapter registry: %w", err)
			}
			return nil
		}
	}

	if old != nil {
		stopAdapterProcess(old, adapterShutdownWait)
	}

	if !next.Enabled {
		c.mu.Lock()
		c.proc = nil
		c.current = next
		c.mu.Unlock()
		c.log.Info("adapter.config_reload.disabled",
			"component", "adapter",
		)
		return nil
	}

	proc := startAdapterProcess(c.log, srv)
	c.mu.Lock()
	c.proc = proc
	c.current = next
	c.mu.Unlock()
	c.log.Info("adapter.config_reload.applied",
		"component", "adapter",
		"enabled", next.Enabled,
		"host", next.Adapter.Host,
		"port", next.Adapter.Port,
		"default_model", next.Adapter.DefaultModel,
	)
	return nil
}

func (c *adapterController) stop() {
	c.mu.Lock()
	proc := c.proc
	c.proc = nil
	c.mu.Unlock()
	if proc != nil {
		stopAdapterProcess(proc, adapterShutdownWait)
	}
}

func stopAdapterProcess(proc *adapterProcess, timeout time.Duration) {
	if proc == nil {
		return
	}
	proc.cancel()
	select {
	case <-proc.done:
	case <-time.After(timeout):
	}
}

func startAdapterProcess(log *slog.Logger, srv *adapter.Server) *adapterProcess {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Start(ctx); err != nil {
			log.Error("adapter.exited",
				"component", "adapter",
				slog.Any("err", err),
			)
		}
	}()
	return &adapterProcess{
		cancel: cancel,
		done:   done,
	}
}
