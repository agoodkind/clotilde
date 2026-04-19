package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"

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

// Run starts the daemon gRPC server on the XDG runtime Unix socket
// and, when the user enables it, the OpenAI compatible HTTP adapter
// on a local port. A single launchd entry boots both layers so the
// monolith stays one process. Additional opt-in background loops
// (prune, oauth refresh) are passed in by the caller.
func Run(log *slog.Logger, extraLoops ...ExtraLoop) error {
	if err := config.EnsureRuntimeDir(); err != nil {
		return err
	}

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

	adapterCancel, err := startAdapter(log)
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
// or impersonation triplet). The daemon then exits non-zero so
// launchd reports the failure instead of silently running without
// the OpenAI surface the user asked for.
func startAdapter(log *slog.Logger) (func(), error) {
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
	deps := adapter.Deps{
		ResolveClaude: findRealClaude,
		ScratchDir:    adapterScratchDir,
	}
	srv, err := adapter.New(cfg.Adapter, deps, log)
	if err != nil {
		log.Error("adapter.registry.invalid_config",
			"component", "adapter",
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("adapter registry: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := srv.Start(ctx); err != nil {
			log.Error("adapter.exited",
				"component", "adapter",
				slog.Any("err", err),
			)
		}
	}()
	return cancel, nil
}
