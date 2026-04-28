package mitm

import (
	"crypto/tls"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// isWebsocketUpgrade reports whether the request asks for a
// websocket protocol upgrade. Matching is case-insensitive on the
// "Connection: upgrade" and "Upgrade: websocket" headers.
func isWebsocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
				return true
			}
		}
	}
	return false
}

// handleWebsocket bridges a client websocket connection to the
// upstream and records every text frame to the JSONL capture
// stream. The schema matches the dump.py mitmproxy addon under
// research/codex/captures/2026-04-27/ so existing captures and new
// captures slot into the same toolchain.
//
// On error at any stage we close both ends and emit a ws_end record
// with the error message in the close field.
func (p *Proxy) handleWebsocket(w http.ResponseWriter, r *http.Request, upstream string) {
	cfg := p.config()

	// Build the upstream URL in ws scheme.
	upstreamURL := upstream + r.URL.RequestURI()
	switch {
	case strings.HasPrefix(upstreamURL, "https://"):
		upstreamURL = "wss://" + strings.TrimPrefix(upstreamURL, "https://")
	case strings.HasPrefix(upstreamURL, "http://"):
		upstreamURL = "ws://" + strings.TrimPrefix(upstreamURL, "http://")
	}

	// Forward all client headers to the upstream handshake except
	// the ws control headers gorilla/websocket sets itself. The
	// websocket library rejects requests carrying these.
	upstreamHeaders := http.Header{}
	for key, values := range r.Header {
		switch strings.ToLower(key) {
		case "upgrade", "connection", "sec-websocket-key",
			"sec-websocket-version", "sec-websocket-extensions",
			"sec-websocket-protocol":
			continue
		}
		for _, value := range values {
			upstreamHeaders.Add(key, value)
		}
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
		TLSClientConfig:  &tls.Config{},
	}
	upstreamConn, upstreamResp, err := dialer.DialContext(r.Context(), upstreamURL, upstreamHeaders)
	if err != nil {
		status := http.StatusBadGateway
		if upstreamResp != nil {
			status = upstreamResp.StatusCode
		}
		p.log.Warn("mitm.ws.dial_failed", "url", upstreamURL, "status", status, "err", err)
		http.Error(w, "ws upstream dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstreamConn.Close()

	upgrader := websocket.Upgrader{
		ReadBufferSize:    32 * 1024,
		WriteBufferSize:   32 * 1024,
		CheckOrigin:       func(*http.Request) bool { return true },
		EnableCompression: false,
	}
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		p.log.Warn("mitm.ws.upgrade_failed", "err", err)
		return
	}
	defer clientConn.Close()

	startEvent := map[string]any{
		"kind":             "ws_start",
		"t":                time.Now().Unix(),
		"url":              upstreamURL,
		"request_headers":  redactHeaders(r.Header),
		"response_headers": redactHeaders(upstreamResp.Header),
	}
	if err := appendCapture(cfg.CaptureDir, startEvent); err != nil {
		p.log.Warn("mitm.ws.capture_start_failed", "err", err)
	}

	var (
		messageCount  int
		messageMu     sync.Mutex
		closeOnce     sync.Once
		closeErr      error
		closeChan     = make(chan struct{})
		captureDir    = cfg.CaptureDir
	)

	closeBoth := func(reason error) {
		closeOnce.Do(func() {
			closeErr = reason
			_ = clientConn.Close()
			_ = upstreamConn.Close()
			close(closeChan)
		})
	}

	relay := func(src, dst *websocket.Conn, fromClient bool) {
		for {
			messageType, payload, err := src.ReadMessage()
			if err != nil {
				closeBoth(err)
				return
			}
			messageMu.Lock()
			messageCount++
			count := messageCount
			messageMu.Unlock()
			text := ""
			if messageType == websocket.TextMessage {
				text = string(payload)
			}
			ev := map[string]any{
				"kind":        "ws_msg",
				"t":           time.Now().Unix(),
				"url":         upstreamURL,
				"from_client": fromClient,
				"len":         len(payload),
				"text":        text,
				"seq":         count,
			}
			if err := appendCapture(captureDir, ev); err != nil {
				p.log.Warn("mitm.ws.capture_msg_failed", "err", err)
			}
			if err := dst.WriteMessage(messageType, payload); err != nil {
				closeBoth(err)
				return
			}
		}
	}

	go relay(clientConn, upstreamConn, true)
	go relay(upstreamConn, clientConn, false)

	<-closeChan

	endEvent := map[string]any{
		"kind":     "ws_end",
		"t":        time.Now().Unix(),
		"url":      upstreamURL,
		"messages": messageCount,
	}
	if closeErr != nil {
		endEvent["err"] = closeErr.Error()
	}
	if err := appendCapture(captureDir, endEvent); err != nil {
		p.log.Warn("mitm.ws.capture_end_failed", "err", err)
	}
	p.log.Info("mitm.ws.closed", "url", upstreamURL, "messages", messageCount)
}
