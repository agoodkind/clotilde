package mitm

import (
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// handleConnect implements RFC 7230 section 4.3.6 HTTP CONNECT
// tunneling. Clients like codex-cli reach wss://chatgpt.com through
// the proxy by issuing CONNECT chatgpt.com:443 and then speaking
// TLS+websocket on the resulting stream. Without this handler the
// default mux returns 404 and the client cannot establish the
// upstream connection.
//
// Tunnel mode forwards opaque bytes in both directions. We do not
// terminate TLS, so the tunneled bytes are end-to-end encrypted; the
// payload is not captured in the JSONL transcript. The capture path
// only sees the CONNECT request line and the duration of the
// tunnel. This is intentional: terminating TLS would require a
// per-host certificate and cert pinning would break the upstream's
// trust anyway.
//
// Drift detection for upstreams that exclusively use CONNECT (e.g.
// codex-cli's wss://chatgpt.com path) needs an external mitmproxy
// session because we deliberately do not MITM-decrypt at this
// layer.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	target := strings.TrimSpace(r.RequestURI)
	if target == "" {
		target = strings.TrimSpace(r.Host)
	}
	if target == "" {
		http.Error(w, "missing CONNECT target", http.StatusBadRequest)
		return
	}
	host, _, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		http.Error(w, "invalid CONNECT target: "+target, http.StatusBadRequest)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	upstream, err := net.DialTimeout("tcp", target, 30*time.Second)
	if err != nil {
		p.log.Warn("mitm.connect.upstream_dial_failed",
			"target", target,
			"err", err,
		)
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	clientConn, bufrw, err := hijacker.Hijack()
	if err != nil {
		p.log.Warn("mitm.connect.hijack_failed", "target", target, "err", err)
		_ = upstream.Close()
		return
	}
	defer clientConn.Close()

	// Tell the client the tunnel is established. The client will
	// follow with TLS handshake + websocket frames.
	if _, err := bufrw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		p.log.Warn("mitm.connect.write_established_failed", "err", err)
		return
	}
	if err := bufrw.Flush(); err != nil {
		p.log.Warn("mitm.connect.flush_failed", "err", err)
		return
	}

	p.log.Info("mitm.connect.tunnel_open",
		"target", target,
		"host", host,
	)
	bytesUp, bytesDown := spliceConnections(clientConn, upstream)
	p.log.Info("mitm.connect.tunnel_closed",
		"target", target,
		"host", host,
		"duration_ms", time.Since(started).Milliseconds(),
		"bytes_up", bytesUp,
		"bytes_down", bytesDown,
	)
}

// spliceConnections forwards bytes between two connections in both
// directions until one side closes. Returns the count of bytes that
// flowed in each direction.
func spliceConnections(client, upstream net.Conn) (bytesUp, bytesDown int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	var upN, downN int64
	go func() {
		defer wg.Done()
		n, _ := io.Copy(upstream, client)
		upN = n
		// Half-close so the upstream's read returns EOF instead of
		// hanging when the client closes its write side.
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(client, upstream)
		downN = n
		if cw, ok := client.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
	return upN, downN
}
