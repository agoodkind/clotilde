package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
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
// reloadHTTPDrainWait caps how long the reload waits for in-flight
// adapter requests to finish before force-closing. The drain polls
// active-request count rather than a flat sleep, so a tunnel that sits
// idle (Cloudflare keep-alive with no Cursor traffic) returns
// immediately. A live agent turn can run for a minute or more, so the
// max wait is generous; it only matters when a real stream is in
// flight.
const reloadHTTPDrainWait = 10 * time.Minute
const reloadGRPCDrainWait = 10 * time.Minute
const envDaemonReloadChild = "CLYDE_DAEMON_RELOAD_CHILD"
const envDaemonInheritedListeners = "CLYDE_DAEMON_INHERITED_LISTENERS"
const envDaemonReadyFD = "CLYDE_DAEMON_READY_FD"

const (
	listenerNameDaemon  = "daemon"
	listenerNameAdapter = "adapter"
	listenerNameWebApp  = "webapp"
)

var errReloadBeforeProcessLock = errors.New("daemon reload is unavailable until this daemon owns the process lock")

type adapterLaunchConfig struct {
	Enabled bool
	Adapter config.AdapterConfig
	Logging config.LoggingConfig
}

type adapterProcess struct {
	cancel        context.CancelFunc
	drain         func(context.Context) error
	forceClose    func() error
	closeListener func() error
	done          chan struct{}
	lis           net.Listener
}

type adapterController struct {
	log     *slog.Logger
	deps    adapter.Deps
	mu      sync.Mutex
	current adapterLaunchConfig
	proc    *adapterProcess
}

type inheritedListenerSpec struct {
	Name    string `json:"name"`
	Network string `json:"network"`
	Addr    string `json:"addr"`
	FD      int    `json:"fd"`
}

type inheritedRuntime struct {
	listeners map[string]net.Listener
	ready     *os.File
}

type webAppProcess struct {
	cancel        func()
	drain         func(context.Context) error
	forceClose    func() error
	closeListener func() error
	done          chan struct{}
	lis           net.Listener
	cfg           config.WebAppConfig
}

type daemonRuntime struct {
	listener   net.Listener
	adapter    *adapterController
	webapp     *webAppProcess
	reloadLock sync.Mutex
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
	reloadChild := os.Getenv(envDaemonReloadChild) == "1"
	var lockHeld atomic.Bool
	var lockReleaseOnce sync.Once
	lockAcquired := make(chan struct{})
	if reloadChild {
		go acquireReloadChildProcessLock(log, lockFile, lockPath, &lockHeld, lockAcquired)
	} else {
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = lockFile.Close()
			log.Info("daemon.already_running",
				"component", "daemon",
				"lock_path", lockPath)
			return nil
		}
		lockHeld.Store(true)
		close(lockAcquired)
	}
	releaseProcessLock := func(reason string) {
		lockReleaseOnce.Do(func() {
			if lockHeld.Swap(false) {
				if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
					log.Warn("daemon.process_lock.release_failed",
						"component", "daemon",
						"lock_path", lockPath,
						"reason", reason,
						"err", err,
					)
				} else {
					log.Info("daemon.process_lock.released",
						"component", "daemon",
						"lock_path", lockPath,
						"reason", reason,
					)
				}
			}
			_ = lockFile.Close()
		})
	}
	defer releaseProcessLock("exit")

	socketPath := config.DaemonSocketPath()

	inherited, err := loadInheritedRuntime()
	if err != nil {
		return fmt.Errorf("load inherited listeners: %w", err)
	}
	listener, err := daemonListener(socketPath, inherited.listeners[listenerNameDaemon])
	if err != nil {
		return err
	}

	srv, err := New(log)
	if err != nil {
		return fmt.Errorf("failed to create daemon server: %w", err)
	}
	defer srv.Close()

	grpcServer := grpc.NewServer()
	clydev1.RegisterClydeServiceServer(grpcServer, srv)

	rt := &daemonRuntime{listener: listener}

	adapterCtrl, adapterCancel, err := startAdapter(log, srv, inherited.listeners[listenerNameAdapter])
	if err != nil {
		return fmt.Errorf("adapter startup: %w", err)
	}
	rt.adapter = adapterCtrl
	webProc, err := startWebApp(log, srv, inherited.listeners[listenerNameWebApp])
	if err != nil {
		if adapterCancel != nil {
			adapterCancel()
		}
		return fmt.Errorf("webapp startup: %w", err)
	}
	rt.webapp = webProc

	var exclusiveMu sync.Mutex
	var exclusiveCancels []func()
	var exclusiveStopped bool
	var exclusiveStopOnce sync.Once
	stopExclusiveLoops := func(reason string) {
		exclusiveStopOnce.Do(func() {
			exclusiveMu.Lock()
			exclusiveStopped = true
			cancels := append([]func(){}, exclusiveCancels...)
			exclusiveCancels = nil
			exclusiveMu.Unlock()
			for i := len(cancels) - 1; i >= 0; i-- {
				cancels[i]()
			}
			log.Info("daemon.exclusive_subsystems.stopped",
				"component", "daemon",
				"reason", reason,
			)
		})
	}
	defer stopExclusiveLoops("exit")
	if adapterCancel != nil {
		exclusiveMu.Lock()
		exclusiveCancels = append(exclusiveCancels, adapterCancel)
		exclusiveMu.Unlock()
	}
	if webProc != nil && webProc.cancel != nil {
		exclusiveMu.Lock()
		exclusiveCancels = append(exclusiveCancels, webProc.cancel)
		exclusiveMu.Unlock()
	}

	startExclusiveLoops := func() {
		exclusiveMu.Lock()
		if exclusiveStopped {
			exclusiveMu.Unlock()
			return
		}
		for _, loop := range extraLoops {
			if loop == nil {
				continue
			}
			if cancel := loop(log); cancel != nil {
				exclusiveCancels = append(exclusiveCancels, cancel)
			}
		}
		exclusiveMu.Unlock()
		log.Info("daemon.exclusive_subsystems.started",
			"component", "daemon",
			"reload_child", reloadChild,
		)
	}
	if reloadChild {
		go func() {
			<-lockAcquired
			startExclusiveLoops()
		}()
	} else {
		startExclusiveLoops()
	}

	setReloadFuncWhenProcessOwner(srv, &lockHeld, func(ctx context.Context) (reloadReport, error) {
		return reloadDaemonBinary(ctx, log, grpcServer, rt, srv, stopExclusiveLoops, releaseProcessLock)
	})

	log.Info("daemon.listening",
		"component", "daemon",
		"socket", socketPath,
	)
	if inherited.ready != nil {
		errCh := make(chan error, 1)
		go func() { errCh <- grpcServer.Serve(listener) }()
		_, _ = inherited.ready.WriteString("ready\n")
		_ = inherited.ready.Close()
		return <-errCh
	}
	return grpcServer.Serve(listener)
}

func setReloadFuncWhenProcessOwner(srv *Server, lockHeld *atomic.Bool, fn func(context.Context) (reloadReport, error)) {
	srv.SetReloadFunc(func(ctx context.Context) (reloadReport, error) {
		if lockHeld == nil || !lockHeld.Load() {
			return reloadReport{}, errReloadBeforeProcessLock
		}
		return fn(ctx)
	})
}

func acquireReloadChildProcessLock(log *slog.Logger, lockFile *os.File, lockPath string, lockHeld *atomic.Bool, lockAcquired chan<- struct{}) {
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		log.Warn("daemon.reload_child.lock_failed",
			"component", "daemon",
			"lock_path", lockPath,
			"err", err,
		)
		return
	}
	lockHeld.Store(true)
	close(lockAcquired)
	log.Info("daemon.reload_child.lock_acquired",
		"component", "daemon",
		"lock_path", lockPath,
	)
}

func daemonListener(socketPath string, inherited net.Listener) (net.Listener, error) {
	if inherited != nil {
		if unixListener, ok := inherited.(*net.UnixListener); ok {
			unixListener.SetUnlinkOnClose(false)
		}
		return inherited, nil
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to remove stale socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", socketPath, err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	return listener, nil
}

func loadInheritedRuntime() (inheritedRuntime, error) {
	out := inheritedRuntime{listeners: make(map[string]net.Listener)}
	raw := os.Getenv(envDaemonInheritedListeners)
	if raw == "" {
		return out, nil
	}
	var specs []inheritedListenerSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return out, fmt.Errorf("decode listener specs: %w", err)
	}
	for _, spec := range specs {
		file := os.NewFile(uintptr(spec.FD), spec.Name)
		if file == nil {
			return out, fmt.Errorf("listener %s fd %d unavailable", spec.Name, spec.FD)
		}
		lis, err := net.FileListener(file)
		_ = file.Close()
		if err != nil {
			return out, fmt.Errorf("listener %s from fd %d: %w", spec.Name, spec.FD, err)
		}
		if lis.Addr().Network() != spec.Network || lis.Addr().String() != spec.Addr {
			_ = lis.Close()
			return out, fmt.Errorf("listener %s inherited as %s/%s, expected %s/%s", spec.Name, lis.Addr().Network(), lis.Addr().String(), spec.Network, spec.Addr)
		}
		if unixListener, ok := lis.(*net.UnixListener); ok {
			unixListener.SetUnlinkOnClose(false)
		}
		out.listeners[spec.Name] = lis
	}
	if rawFD := os.Getenv(envDaemonReadyFD); rawFD != "" {
		fd, err := strconv.Atoi(rawFD)
		if err != nil {
			return out, fmt.Errorf("parse ready fd: %w", err)
		}
		out.ready = os.NewFile(uintptr(fd), "daemon-reload-ready")
		if out.ready == nil {
			return out, fmt.Errorf("ready fd %d unavailable", fd)
		}
	}
	return out, nil
}

func reloadDaemonBinary(ctx context.Context, log *slog.Logger, grpcServer *grpc.Server, rt *daemonRuntime, srv *Server, stopExclusive func(string), releaseProcessLock func(string)) (reloadReport, error) {
	rt.reloadLock.Lock()
	defer rt.reloadLock.Unlock()
	executablePath, err := os.Executable()
	if err != nil {
		return reloadReport{}, fmt.Errorf("resolve executable: %w", err)
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return reloadReport{}, fmt.Errorf("resolve executable path: %w", err)
	}
	if info, err := os.Stat(executablePath); err != nil {
		return reloadReport{}, fmt.Errorf("stat executable: %w", err)
	} else if info.IsDir() {
		return reloadReport{}, fmt.Errorf("executable path is a directory: %s", executablePath)
	}

	files, specs, cleanup, err := inheritedListenerFiles(rt)
	if err != nil {
		return reloadReport{}, err
	}
	defer cleanup()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		return reloadReport{}, fmt.Errorf("create reload readiness pipe: %w", err)
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	readyFD := 3 + len(files)

	cmd := exec.Command(executablePath, "daemon")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.ExtraFiles = append(files, readyWrite)
	specJSON, err := json.Marshal(specs)
	if err != nil {
		return reloadReport{}, fmt.Errorf("encode inherited listeners: %w", err)
	}
	cmd.Env = append(os.Environ(),
		envDaemonReloadChild+"=1",
		envDaemonInheritedListeners+"="+string(specJSON),
		envDaemonReadyFD+"="+strconv.Itoa(readyFD),
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return reloadReport{}, fmt.Errorf("start replacement daemon: %w", err)
	}
	_ = readyWrite.Close()
	go func() { _ = cmd.Wait() }()

	if err := waitForReplacementDaemon(ctx, readyRead); err != nil {
		_ = cmd.Process.Kill()
		return reloadReport{}, err
	}
	srv.preserveRuntimeDirsOnClose()
	drainReloadedPublicHTTP(log, rt)
	if stopExclusive != nil {
		stopExclusive("reload_handoff")
	}
	grpcDrainStarted := make(chan struct{})
	go func() {
		log.Info("daemon.reload.draining_old_process",
			"component", "daemon",
			"new_pid", cmd.Process.Pid,
			"timeout", reloadGRPCDrainWait.String(),
		)
		done := make(chan struct{})
		go func() {
			close(grpcDrainStarted)
			grpcServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
			log.Info("daemon.reload.old_process_grpc_drain_complete",
				"component", "daemon",
				"new_pid", cmd.Process.Pid,
			)
		case <-time.After(reloadGRPCDrainWait):
			log.Warn("daemon.reload.old_process_grpc_drain_timeout",
				"component", "daemon",
				"new_pid", cmd.Process.Pid,
				"timeout", reloadGRPCDrainWait.String(),
			)
			grpcServer.Stop()
			<-done
		}
	}()
	<-grpcDrainStarted
	if releaseProcessLock != nil {
		releaseProcessLock("reload_handoff")
	}
	return reloadReport{BinaryReloaded: true, NewPID: cmd.Process.Pid}, nil
}

func drainReloadedPublicHTTP(log *slog.Logger, rt *daemonRuntime) {
	if rt == nil {
		return
	}
	if rt.adapter != nil {
		rt.adapter.drainReloadedProcess(reloadHTTPDrainWait)
	}
	if rt.webapp != nil && rt.webapp.drain != nil {
		log.Info("daemon.reload.draining_old_webapp",
			"component", "daemon",
			"addr", listenerAddr(rt.webapp.lis),
		)
		if rt.webapp.closeListener != nil {
			if err := rt.webapp.closeListener(); err != nil && !errors.Is(err, net.ErrClosed) {
				log.Warn("daemon.reload.webapp_listener_close_failed",
					"component", "daemon",
					"addr", listenerAddr(rt.webapp.lis),
					"err", err,
				)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), reloadHTTPDrainWait)
		err := rt.webapp.drain(ctx)
		cancel()
		if err != nil {
			log.Warn("daemon.reload.webapp_drain_timeout",
				"component", "daemon",
				"addr", listenerAddr(rt.webapp.lis),
				"err", err,
			)
		} else {
			log.Info("daemon.reload.webapp_drain_complete",
				"component", "daemon",
				"addr", listenerAddr(rt.webapp.lis),
			)
		}
		if rt.webapp.forceClose != nil {
			if err := rt.webapp.forceClose(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				log.Warn("daemon.reload.webapp_force_close_failed",
					"component", "daemon",
					"addr", listenerAddr(rt.webapp.lis),
					"err", err,
				)
			} else if err != nil {
				log.Debug("daemon.reload.webapp_force_closed",
					"component", "daemon",
					"addr", listenerAddr(rt.webapp.lis),
				)
			}
		}
	}
}

func waitForReplacementDaemon(ctx context.Context, ready io.Reader) error {
	deadlineCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		data, err := io.ReadAll(ready)
		if err != nil {
			done <- err
			return
		}
		if string(data) != "ready\n" {
			done <- fmt.Errorf("replacement daemon readiness failed: %q", string(data))
			return
		}
		done <- nil
	}()
	select {
	case <-deadlineCtx.Done():
		return fmt.Errorf("replacement daemon did not become ready: %w", deadlineCtx.Err())
	case err := <-done:
		return err
	}
}

func inheritedListenerFiles(rt *daemonRuntime) ([]*os.File, []inheritedListenerSpec, func(), error) {
	if rt == nil || rt.listener == nil {
		return nil, nil, func() {}, fmt.Errorf("daemon listener is not available for reload")
	}
	if err := validateReloadListenerConfig(rt); err != nil {
		return nil, nil, func() {}, err
	}
	type namedListener struct {
		name string
		lis  net.Listener
	}
	listeners := []namedListener{{name: listenerNameDaemon, lis: rt.listener}}
	if rt.adapter != nil {
		if lis := rt.adapter.listener(); lis != nil {
			listeners = append(listeners, namedListener{name: listenerNameAdapter, lis: lis})
		}
	}
	if rt.webapp != nil && rt.webapp.lis != nil {
		listeners = append(listeners, namedListener{name: listenerNameWebApp, lis: rt.webapp.lis})
	}
	files := make([]*os.File, 0, len(listeners))
	specs := make([]inheritedListenerSpec, 0, len(listeners))
	cleanup := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, named := range listeners {
		file, err := listenerFile(named.lis)
		if err != nil {
			cleanup()
			return nil, nil, func() {}, fmt.Errorf("inherit listener %s: %w", named.name, err)
		}
		files = append(files, file)
		specs = append(specs, inheritedListenerSpec{
			Name:    named.name,
			Network: named.lis.Addr().Network(),
			Addr:    named.lis.Addr().String(),
			FD:      3 + len(files) - 1,
		})
	}
	return files, specs, cleanup, nil
}

func listenerFile(lis net.Listener) (*os.File, error) {
	switch l := lis.(type) {
	case *net.UnixListener:
		return l.File()
	case *net.TCPListener:
		return l.File()
	default:
		return nil, fmt.Errorf("unsupported listener type %T", lis)
	}
}

func listenerAddr(lis net.Listener) string {
	if lis == nil {
		return ""
	}
	return lis.Addr().String()
}

func validateReloadListenerConfig(rt *daemonRuntime) error {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		return fmt.Errorf("load config for reload validation: %w", err)
	}
	adapterRunning := rt.adapter != nil && rt.adapter.listener() != nil
	if adapterRunning != cfg.Adapter.Enabled {
		return fmt.Errorf("adapter listener set changed; full daemon restart required")
	}
	if adapterRunning {
		if got, want := rt.adapter.listener().Addr().String(), adapterListenAddr(cfg.Adapter); got != want {
			return fmt.Errorf("adapter listen address changed from %s to %s; full daemon restart required", got, want)
		}
	}
	webRunning := rt.webapp != nil && rt.webapp.lis != nil
	if webRunning != cfg.WebApp.Enabled {
		return fmt.Errorf("webapp listener set changed; full daemon restart required")
	}
	if webRunning {
		if got, want := rt.webapp.lis.Addr().String(), webAppListenAddr(cfg.WebApp); got != want {
			return fmt.Errorf("webapp listen address changed from %s to %s; full daemon restart required", got, want)
		}
	}
	return nil
}

func adapterListenAddr(cfg config.AdapterConfig) string {
	host := cfg.Host
	if host == "" {
		host = adapter.DefaultHost
	}
	port := cfg.Port
	if port <= 0 {
		port = adapter.DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func webAppListenAddr(cfg config.WebAppConfig) string {
	host := cfg.Host
	if host == "" {
		host = webapp.DefaultHost
	}
	port := cfg.Port
	if port <= 0 {
		port = webapp.DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// startWebApp boots the optional remote dashboard. The webapp shares
// the daemon process so a single launchd entry covers gRPC, the
// OpenAI adapter, and the dashboard.
func startWebApp(log *slog.Logger, srv *Server, inherited net.Listener) (*webAppProcess, error) {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		log.Warn("webapp.config_load_failed",
			"component", "webapp",
			"err", err,
		)
		return nil, nil
	}
	if !cfg.WebApp.Enabled {
		if inherited != nil {
			return nil, fmt.Errorf("webapp listener inherited but webapp is disabled; full daemon restart required")
		}
		return nil, nil
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
		StartRemoteSession: func(ctx context.Context, name, basedir string) (string, string, error) {
			resp, err := srv.StartRemoteSession(ctx, &clydev1.StartRemoteSessionRequest{
				SessionName: name,
				Basedir:     basedir,
			})
			if err != nil {
				return "", "", err
			}
			return resp.GetSessionName(), resp.GetSessionId(), nil
		},
	}
	srvW := webapp.New(cfg.WebApp, deps, log)
	lis := inherited
	if lis != nil {
		if got, want := lis.Addr().String(), srvW.Addr(); got != want {
			return nil, fmt.Errorf("webapp inherited listener address %s does not match config %s; full daemon restart required", got, want)
		}
	} else {
		lis, err = net.Listen("tcp", srvW.Addr())
		if err != nil {
			return nil, fmt.Errorf("webapp listen %s: %w", srvW.Addr(), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srvW.StartOnListener(ctx, lis); err != nil {
			log.Error("webapp.exited",
				"component", "webapp",
				"err", err,
			)
		}
	}()
	return &webAppProcess{
		cancel:        cancel,
		drain:         srvW.Shutdown,
		forceClose:    srvW.Close,
		closeListener: lis.Close,
		done:          done,
		lis:           lis,
		cfg:           cfg.WebApp,
	}, nil
}

// startAdapter reads the global config and launches the HTTP server
// when enabled. A cancel func is returned so Run can shut the
// listener down on exit.
//
// Returns an error when the adapter is enabled but
// adapter.New rejects the config (missing families, default model,
// or required client_identity fields). The daemon then exits non-zero so
// launchd reports the failure instead of silently running without
// the OpenAI surface the user asked for.
func startAdapter(log *slog.Logger, srv *Server, inherited net.Listener) (*adapterController, func(), error) {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		log.Warn("adapter.config_load_failed",
			"component", "adapter",
			"err", err,
		)
		return nil, nil, nil
	}

	ctrl := &adapterController{
		log: log,
		deps: adapter.Deps{
			ResolveClaude: findRealClaude,
			ScratchDir:    adapterScratchDir,
			RequestEvents: srv.providerStats.Record,
		},
	}
	if err := ctrl.apply(launchConfigFromGlobal(cfg), true, inherited); err != nil {
		return nil, nil, err
	}

	stopWatch, err := watchAdapterConfig(log, ctrl)
	if err != nil {
		log.Warn("adapter.config_watch_failed",
			"component", "adapter",
			"err", err,
		)
	}
	return ctrl, func() {
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
					"err", err,
				)
				return
			}
			if err := ctrl.apply(launchConfigFromGlobal(cfg), false, nil); err != nil {
				log.Warn("adapter.config_reload_apply_failed",
					"component", "adapter",
					"err", err,
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
					"err", err,
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

func (c *adapterController) apply(next adapterLaunchConfig, startup bool, inherited net.Listener) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.current
	if !startup && reflect.DeepEqual(prev, next) {
		if inherited != nil {
			_ = inherited.Close()
		}
		c.log.Debug("adapter.config_reload.noop",
			"component", "adapter",
		)
		return nil
	}
	old := c.proc

	var srv *adapter.Server
	var err error
	if next.Enabled {
		srv, err = adapter.New(next.Adapter, next.Logging, c.deps, c.log)
		if err != nil {
			c.log.Error("adapter.registry.invalid_config",
				"component", "adapter",
				"err", err,
			)
			if startup {
				return fmt.Errorf("adapter registry: %w", err)
			}
			return nil
		}
		if inherited != nil {
			if got, want := inherited.Addr().String(), srv.Addr(); got != want {
				return fmt.Errorf("adapter inherited listener address %s does not match config %s; full daemon restart required", got, want)
			}
		}
	} else if inherited != nil {
		return fmt.Errorf("adapter listener inherited but adapter is disabled; full daemon restart required")
	}

	if old != nil {
		stopAdapterProcess(old, adapterShutdownWait)
	}

	if !next.Enabled {
		c.proc = nil
		c.current = next
		c.log.Info("adapter.config_reload.disabled",
			"component", "adapter",
		)
		return nil
	}

	lis := inherited
	if lis == nil {
		lis, err = net.Listen("tcp", srv.Addr())
		if err != nil {
			return fmt.Errorf("adapter listen %s: %w", srv.Addr(), err)
		}
	}
	proc := startAdapterProcess(c.log, srv, lis)
	c.proc = proc
	c.current = next
	c.log.Info("adapter.config_reload.applied",
		"component", "adapter",
		"enabled", next.Enabled,
		"host", next.Adapter.Host,
		"port", next.Adapter.Port,
		"default_model", next.Adapter.DefaultModel,
	)
	return nil
}

func (c *adapterController) listener() net.Listener {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proc == nil {
		return nil
	}
	return c.proc.lis
}

func (c *adapterController) drainReloadedProcess(timeout time.Duration) {
	c.mu.Lock()
	proc := c.proc
	c.mu.Unlock()
	if proc == nil || proc.drain == nil {
		return
	}
	c.log.Info("daemon.reload.draining_old_adapter",
		"component", "daemon",
		"addr", listenerAddr(proc.lis),
	)
	if proc.closeListener != nil {
		if err := proc.closeListener(); err != nil && !errors.Is(err, net.ErrClosed) {
			c.log.Warn("daemon.reload.adapter_listener_close_failed",
				"component", "daemon",
				"addr", listenerAddr(proc.lis),
				"err", err,
			)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := proc.drain(ctx)
	cancel()
	if err != nil {
		c.log.Warn("daemon.reload.adapter_drain_timeout",
			"component", "daemon",
			"addr", listenerAddr(proc.lis),
			"err", err,
		)
	} else {
		c.log.Info("daemon.reload.adapter_drain_complete",
			"component", "daemon",
			"addr", listenerAddr(proc.lis),
		)
	}
	if proc.forceClose != nil {
		if err := proc.forceClose(); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			c.log.Warn("daemon.reload.adapter_force_close_failed",
				"component", "daemon",
				"addr", listenerAddr(proc.lis),
				"err", err,
			)
		} else if err != nil {
			c.log.Debug("daemon.reload.adapter_force_closed",
				"component", "daemon",
				"addr", listenerAddr(proc.lis),
			)
		}
	}
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

func startAdapterProcess(log *slog.Logger, srv *adapter.Server, lis net.Listener) *adapterProcess {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.StartOnListener(ctx, lis); err != nil {
			log.Error("adapter.exited",
				"component", "adapter",
				"err", err,
			)
		}
	}()
	return &adapterProcess{
		cancel:        cancel,
		drain:         srv.Shutdown,
		forceClose:    srv.Close,
		closeListener: lis.Close,
		done:          done,
		lis:           lis,
	}
}
