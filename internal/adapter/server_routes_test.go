package adapter

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"goodkind.io/clyde/internal/config"
)

func TestStartOnListenerServesHealth(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	cfg := baseConfig()
	cfg.Enabled = true
	srv, err := New(cfg, config.LoggingConfig{}, Deps{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.StartOnListener(ctx, lis) }()

	resp, err := http.Get("http://" + lis.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not stop")
	}
}
