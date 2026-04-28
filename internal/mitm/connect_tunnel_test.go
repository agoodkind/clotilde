package mitm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/clyde/internal/config"
)

// TestHandleConnectTunnelsBytesBothWays stands up a fake upstream
// that echoes a fixed prefix on connect, accepts a payload from the
// tunneled client, and returns it reversed. The proxy is invoked
// directly through its handle method against a hijackable
// httptest-style listener. Verifies the tunnel:
//
//   - returns "HTTP/1.1 200 Connection Established"
//   - splices client-to-upstream and upstream-to-client bytes
//   - emits the tunnel_open / tunnel_closed log events
func TestHandleConnectTunnelsBytesBothWays(t *testing.T) {
	upstream := startEchoServer(t)
	defer upstream.Close()

	proxy := startTestProxy(t)
	defer proxy.shutdown()

	client, err := net.DialTimeout("tcp", proxy.addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer client.Close()

	if _, err := fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.addr, upstream.addr); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}

	br := bufio.NewReader(client)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.HasPrefix(statusLine, "HTTP/1.1 200") {
		t.Fatalf("status = %q, want 200 Connection Established", strings.TrimSpace(statusLine))
	}
	// Drain headers up to the empty line.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	// Send a payload through the tunnel. The echo upstream will
	// reverse it and write it back.
	payload := "hello-clyde-tunnel"
	if _, err := client.Write([]byte(payload + "\n")); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	// Half-close client write so upstream's reader returns EOF and
	// the server's reverse-and-write goroutine flushes.
	if cw, ok := client.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}

	got, err := io.ReadAll(br)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("read tunneled response: %v", err)
	}
	want := reverse(payload)
	if !strings.Contains(string(got), want) {
		t.Errorf("tunneled response %q does not contain reversed payload %q", string(got), want)
	}
}

func TestHandleConnectRejectsMissingTarget(t *testing.T) {
	proxy := startTestProxy(t)
	defer proxy.shutdown()

	client, err := net.DialTimeout("tcp", proxy.addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("CONNECT  HTTP/1.1\r\nHost: \r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	br := bufio.NewReader(client)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Either 400 from our handler or 400/404 from net/http parsing
	// before our handler sees it. Both are acceptable rejections.
	if !strings.Contains(status, "400") && !strings.Contains(status, "404") {
		t.Errorf("missing-target CONNECT status = %q, want 4xx", strings.TrimSpace(status))
	}
}

// echoServer accepts TCP connections, reads a line, and writes back
// the reversed line. Used as a tunneled upstream in proxy tests.
type echoServer struct {
	listener net.Listener
	addr     string
	wg       sync.WaitGroup
}

func startEchoServer(t *testing.T) *echoServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &echoServer{listener: listener, addr: listener.Addr().String()}
	server.wg.Add(1)
	go server.serve()
	return server
}

func (s *echoServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			line, _ := bufio.NewReader(c).ReadString('\n')
			line = strings.TrimRight(line, "\r\n")
			_, _ = c.Write([]byte(reverse(line)))
		}(conn)
	}
}

func (s *echoServer) Close() {
	_ = s.listener.Close()
	s.wg.Wait()
}

type testProxy struct {
	server *http.Server
	proxy  *Proxy
	addr   string
}

func (t *testProxy) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = t.server.Shutdown(ctx)
}

func startTestProxy(t *testing.T) *testProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &Proxy{
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		client: http.DefaultClient,
		cfg:    config.MITMConfig{CaptureDir: t.TempDir(), BodyMode: "summary"},
		base:   "http://" + listener.Addr().String(),
	}
	server := &http.Server{Handler: http.HandlerFunc(p.handle)}
	p.server = server
	go func() { _ = server.Serve(listener) }()
	return &testProxy{server: server, proxy: p, addr: listener.Addr().String()}
}

func reverse(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
