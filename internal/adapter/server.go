package adapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/fallback"
	"goodkind.io/clyde/internal/adapter/oauth"
	"goodkind.io/clyde/internal/config"
)

// DefaultPort is the loopback port the adapter listens on when
// AdapterConfig.Port is zero. The value matches the Ollama default
// so OPENAI_BASE_URL=http://localhost:11434/v1 flows work unchanged.
const DefaultPort = 11434

// DefaultHost is the loopback bind. The adapter never binds a public
// interface unless the user explicitly sets AdapterConfig.Host.
const DefaultHost = "127.0.0.1"

// DefaultMaxConcurrent caps the number of in flight claude
// subprocesses when the config omits a value.
const DefaultMaxConcurrent = 4

// rawChatBodyLimit caps adapter.chat.raw body capture at 256 KiB
// before marking body_truncated=true.
const rawChatBodyLimit = 256 * 1024

type rawChatLogEvent struct {
	RequestID     string
	Method        string
	Path          string
	RemoteAddr    string
	Headers       map[string]string
	BodyBytes     int
	Body          string
	BodyTruncated bool
}

func (attrs rawChatLogEvent) asAttrs() []slog.Attr {
	out := []slog.Attr{
		slog.String("request_id", attrs.RequestID),
		slog.String("method", attrs.Method),
		slog.String("path", attrs.Path),
		slog.String("remote_addr", attrs.RemoteAddr),
		slog.Any("headers", attrs.Headers),
		slog.Int("body_bytes", attrs.BodyBytes),
		slog.String("body", attrs.Body),
	}
	if attrs.BodyTruncated {
		out = append(out, slog.Bool("body_truncated", true))
	}
	return out
}

// systemFingerprint is the value the adapter reports in the OpenAI
// response field of the same name. It changes when the binary is
// rebuilt so clients can detect a behavioral change. Kept stable
// across requests within one daemon run.
var systemFingerprint = "fp_clyde_" + time.Now().UTC().Format("20060102")

// Server is the HTTP facade. The daemon process creates one and
// either calls Start in a goroutine (production) or hands the
// handler to httptest.Server (tests).
type Server struct {
	cfg      config.AdapterConfig
	logprobs config.AdapterLogprobs
	deps     Deps
	log      *slog.Logger
	registry *Registry
	sem      chan struct{}
	token    string
	mux      *http.ServeMux
	httpSrv  *http.Server
	oauthMgr *oauth.Manager
	anthr    *anthropic.Client
	// fb is the optional `claude -p` fallback driver. nil unless
	// cfg.Fallback.Enabled. When set, fbSem caps its concurrency
	// independently of sem.
	fb    *fallback.Client
	fbSem chan struct{}
}

// New constructs a Server from the given adapter config. The deps
// hooks come from the daemon process so the adapter reuses existing
// binary resolution and scratch dir wiring. Returns an error when
// the registry cannot be built (missing families, default model, or
// impersonation triplet); the daemon refuses to start the listener
// in that case.
func New(cfg config.AdapterConfig, deps Deps, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	max := cfg.MaxConcurrent
	if max <= 0 {
		max = DefaultMaxConcurrent
	}
	token := cfg.RequireToken
	if v := os.Getenv("CLYDE_ADAPTER_TOKEN"); v != "" {
		token = v
	}
	registry, err := NewRegistry(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		logprobs: cfg.Logprobs,
		deps:     deps,
		log:      log.With("component", "adapter"),
		registry: registry,
		sem:      make(chan struct{}, max),
		token:    token,
	}
	if cfg.DirectOAuth {
		s.oauthMgr = oauth.NewManager("")
		s.anthr = anthropic.New(nil, s.oauthMgr, anthropic.Config{
			BetaHeader:         cfg.Impersonation.BetaHeader,
			UserAgent:          cfg.Impersonation.UserAgent,
			SystemPromptPrefix: cfg.Impersonation.SystemPromptPrefix,
		})
		s.log.LogAttrs(context.Background(), slog.LevelInfo, "adapter.oauth.enabled",
			slog.Int("max_concurrent", max),
		)
	}
	if cfg.Fallback.Enabled {
		fbCfg, err := buildFallbackConfig(cfg.Fallback, deps)
		if err != nil {
			return nil, fmt.Errorf("adapter: fallback wiring: %w", err)
		}
		s.fb = fallback.New(fbCfg)
		s.fbSem = make(chan struct{}, cfg.Fallback.MaxConcurrent)
		s.log.LogAttrs(context.Background(), slog.LevelInfo, "adapter.fallback.enabled",
			slog.String("trigger", cfg.Fallback.Trigger),
			slog.Int("max_concurrent", cfg.Fallback.MaxConcurrent),
			slog.String("binary", fbCfg.Binary),
			slog.String("scratch", fbCfg.ScratchDir),
			slog.Bool("forward_to_shunt", cfg.Fallback.ForwardToShunt.Enabled),
		)
	}
	s.mux = s.routes()
	return s, nil
}

// buildFallbackConfig resolves runtime values from the user's
// AdapterFallback stanza: the binary path (via deps.ResolveClaude
// when Binary is empty), the parsed timeout, and the scratch
// directory beneath deps.ScratchDir. Failures here abort daemon
// startup the same way an invalid registry does.
func buildFallbackConfig(fb config.AdapterFallback, deps Deps) (fallback.Config, error) {
	bin := fb.Binary
	if bin == "" {
		if deps.ResolveClaude == nil {
			return fallback.Config{}, fmt.Errorf("adapter: fallback.binary empty and deps.ResolveClaude not wired")
		}
		resolved, err := deps.ResolveClaude()
		if err != nil {
			return fallback.Config{}, fmt.Errorf("resolve claude binary: %w", err)
		}
		bin = resolved
	}
	d, err := time.ParseDuration(fb.Timeout)
	if err != nil {
		return fallback.Config{}, fmt.Errorf("parse timeout %q: %w", fb.Timeout, err)
	}
	base := ""
	if deps.ScratchDir != nil {
		base = deps.ScratchDir()
	}
	if base == "" {
		return fallback.Config{}, fmt.Errorf("adapter: deps.ScratchDir returned empty path; required for fallback")
	}
	scratch, err := fallback.EnsureScratchDir(base, fb.ScratchSubdir)
	if err != nil {
		return fallback.Config{}, err
	}
	cfg := fallback.Config{
		Binary:          bin,
		Timeout:         d,
		ScratchDir:      scratch,
		SuppressHookEnv: fb.SuppressHookEnv,
	}
	if err := cfg.Validate(); err != nil {
		return fallback.Config{}, fmt.Errorf("adapter: %w", err)
	}
	return cfg, nil
}

// Addr returns the host:port the adapter will bind when Start is
// called.
func (s *Server) Addr() string {
	host := s.cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := s.cfg.Port
	if port <= 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (s *Server) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for concurrency slot")
	}
}

func (s *Server) release() {
	select {
	case <-s.sem:
	default:
	}
}

var idLock sync.Mutex

func newRequestID() string {
	idLock.Lock()
	defer idLock.Unlock()
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "chatcmpl-" + hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, ErrorResponse{Error: ErrorBody{Message: msg, Type: kind}})
}
