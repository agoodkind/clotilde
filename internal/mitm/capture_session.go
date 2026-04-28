package mitm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"goodkind.io/clyde/internal/config"
)

// CaptureSessionOptions configures one capture run.
type CaptureSessionOptions struct {
	Profile     LaunchProfile
	CaptureRoot string // base dir; the session writes to <root>/<upstream>/<timestamp>/
	CACertPath  string // mitmproxy CA cert path (e.g. ~/.mitmproxy/mitmproxy-ca-cert.pem)
	ProxyHost   string // host:port for the proxy listener (e.g. 127.0.0.1:8888)
	ExtraArgs   []string
	Log         *slog.Logger
}

// CaptureSessionResult reports where the JSONL transcript landed.
type CaptureSessionResult struct {
	TranscriptPath string
	UpstreamBinary string
	StartedAt      time.Time
	EndedAt        time.Time
}

// RunCaptureSession starts the mitm proxy listener, spawns the
// upstream client with the right env and Chromium flags, and waits
// for the upstream binary to exit (or for the parent context to
// cancel). The transcript lands at
// `<CaptureRoot>/<upstream>/<timestamp>/capture.jsonl`.
//
// The caller is responsible for ensuring the mitmproxy CA cert
// already exists at CACertPath. We don't auto-install or auto-trust;
// that is a one-time user setup step (run mitmproxy once to seed the
// cert, then trust it system-wide if needed).
func RunCaptureSession(ctx context.Context, opts CaptureSessionOptions) (CaptureSessionResult, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.CaptureRoot == "" {
		return CaptureSessionResult{}, fmt.Errorf("capture session: CaptureRoot is required")
	}

	binary, err := opts.Profile.ResolvedBinary()
	if err != nil {
		return CaptureSessionResult{}, fmt.Errorf("resolve binary: %w", err)
	}

	timestamp := time.Now().UTC().Format("20060102T150405Z")
	captureDir := filepath.Join(opts.CaptureRoot, opts.Profile.Name, timestamp)
	if err := os.MkdirAll(captureDir, 0o755); err != nil {
		return CaptureSessionResult{}, fmt.Errorf("create capture dir: %w", err)
	}

	// Configure the proxy with the per-session capture dir.
	cfg := config.MITMConfig{
		CaptureDir: captureDir,
		BodyMode:   "raw",
	}

	proxy, err := startCaptureProxy(opts.ProxyHost, cfg, log)
	if err != nil {
		return CaptureSessionResult{}, fmt.Errorf("start proxy: %w", err)
	}
	defer proxy.Shutdown()
	proxyURL := proxy.URL()

	// Compose env vars per profile.
	envOverrides := map[string]string{
		"HTTPS_PROXY":         proxyURL,
		"HTTP_PROXY":          proxyURL,
		"ALL_PROXY":           proxyURL,
		"NO_PROXY":            "",
		"SSL_CERT_FILE":       opts.CACertPath,
		"NODE_EXTRA_CA_CERTS": opts.CACertPath,
	}
	env := opts.Profile.ComposeEnv(os.Environ(), envOverrides)

	args := append([]string{}, opts.Profile.BaseArgs...)
	args = append(args, opts.Profile.ChromiumFlags(proxyURL)...)
	args = append(args, opts.ExtraArgs...)

	startedAt := time.Now()
	log.Info("mitm.capture.session_starting",
		"upstream", opts.Profile.Name,
		"binary", binary,
		"capture_dir", captureDir,
		"proxy_url", proxyURL,
		"is_electron", opts.Profile.IsElectron,
		"args", args,
	)

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	// Forward Ctrl-C through to the upstream so the user can stop a
	// capture cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := cmd.Start(); err != nil {
		return CaptureSessionResult{}, fmt.Errorf("start %s: %w", opts.Profile.Name, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-done
	case <-sigCh:
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-done
	case err := <-done:
		if err != nil && !isExpectedExit(err) {
			log.Warn("mitm.capture.upstream_exited_with_error", "err", err)
		}
	}

	endedAt := time.Now()
	transcript := filepath.Join(captureDir, "capture.jsonl")
	log.Info("mitm.capture.session_ended",
		"upstream", opts.Profile.Name,
		"transcript", transcript,
		"duration_ms", endedAt.Sub(startedAt).Milliseconds(),
	)

	return CaptureSessionResult{
		TranscriptPath: transcript,
		UpstreamBinary: binary,
		StartedAt:      startedAt,
		EndedAt:        endedAt,
	}, nil
}

// isExpectedExit recognizes user-driven shutdown errors from
// exec.Cmd that should not be treated as failures.
func isExpectedExit(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "signal:") || strings.Contains(msg, "killed")
}

// captureProxyHandle wraps a Proxy with its listener for clean
// shutdown.
type captureProxyHandle struct {
	proxy    *Proxy
	listener net.Listener
	server   *http.Server
}

func (h *captureProxyHandle) URL() string {
	return "http://" + h.listener.Addr().String()
}

func (h *captureProxyHandle) Shutdown() {
	if h.server != nil {
		_ = h.server.Close()
	}
}

func startCaptureProxy(addr string, cfg config.MITMConfig, log *slog.Logger) (*captureProxyHandle, error) {
	if addr == "" {
		addr = "[::1]:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	proxy := &Proxy{
		log:    log.With("component", "mitm-capture"),
		client: http.DefaultClient,
		cfg:    cfg,
		base:   "http://" + ln.Addr().String(),
	}
	srv := &http.Server{Handler: http.HandlerFunc(proxy.handle)}
	proxy.server = srv
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Warn("mitm.capture.proxy_serve_failed", "err", err)
		}
	}()
	return &captureProxyHandle{
		proxy:    proxy,
		listener: ln,
		server:   srv,
	}, nil
}
