package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Start binds the TCP listener and serves until ctx is done.
func (s *Server) Start(ctx context.Context) error {
	addr := s.Addr()
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("adapter listen %s: %w", addr, err)
	}
	return s.StartOnListener(ctx, lis)
}

// StartOnListener serves the adapter on an already-bound listener.
// Daemon reload uses this to inherit the existing adapter socket
// without creating a bind gap.
func (s *Server) StartOnListener(ctx context.Context, lis net.Listener) error {
	s.httpSrv = &http.Server{
		Addr:              lis.Addr().String(),
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "adapter listening",
		slog.String("addr", lis.Addr().String()),
		slog.Int("models", len(s.registry.List())),
	)
	errCh := make(chan error, 1)
	go func() { errCh <- s.httpSrv.Serve(lis) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)
		if s.codexSessions != nil {
			s.codexSessions.CloseAll()
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/models", s.auth(s.handleModels))
	mux.HandleFunc("/v1/chat/completions", s.auth(s.handleChat))
	mux.HandleFunc("/v1/completions", s.auth(s.handleLegacy))
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "clyde-openai-adapter",
		"paths":   []string{"/v1/models", "/v1/chat/completions", "/v1/completions", "/healthz"},
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		want := "Bearer " + s.token
		if r.Header.Get("Authorization") != want {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next(w, r)
	}
}
