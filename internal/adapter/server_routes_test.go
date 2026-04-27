package adapter

import (
	"bufio"
	"context"
	"fmt"
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

func TestShutdownClosesIdleKeepaliveConnection(t *testing.T) {
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

	conn, err := net.Dial("tcp", lis.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := fmt.Fprintf(conn, "GET /healthz HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", lis.Addr().String()); err != nil {
		t.Fatalf("write first request: %v", err)
	}
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read first response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", resp.StatusCode)
	}

	shutCtx, shutCancel := context.WithTimeout(context.Background(), time.Second)
	if err := srv.Shutdown(shutCtx); err != nil {
		shutCancel()
		t.Fatalf("shutdown: %v", err)
	}
	shutCancel()

	_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := fmt.Fprintf(conn, "GET /healthz HTTP/1.1\r\nHost: %s\r\nConnection: keep-alive\r\n\r\n", lis.Addr().String()); err == nil {
		if resp, err := http.ReadResponse(reader, nil); err == nil {
			_ = resp.Body.Close()
			t.Fatalf("idle keepalive connection served request after shutdown with status %d", resp.StatusCode)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not stop")
	}
}
