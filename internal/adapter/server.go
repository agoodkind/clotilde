package adapter

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/anthropic/fallback"
	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	"goodkind.io/clyde/internal/adapter/oauth"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
	"goodkind.io/clyde/internal/config"
)

// DefaultPort is the loopback port the adapter listens on when
// AdapterConfig.Port is zero. The value matches the Ollama default
// so OPENAI_BASE_URL=http://localhost:11434/v1 flows work unchanged.
const DefaultPort = 11434

// DefaultHost is the loopback bind. The adapter never binds a public
// interface unless the user explicitly sets AdapterConfig.Host.
const DefaultHost = "::1"

// DefaultMaxConcurrent caps the number of in flight claude
// subprocesses when the config omits a value.
// TODO remove this ? we dont need a max
const DefaultMaxConcurrent = 4
const defaultCodexBaseURL = "https://chatgpt.com/backend-api/codex/responses"

type rawChatLogEvent struct {
	RequestID     string
	Method        string
	Path          string
	RemoteAddr    string
	Headers       map[string]string
	BodyBytes     int
	BodySummary   *BodySummary
	BodyRaw       string
	BodyB64       string
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
	}
	if attrs.BodySummary != nil {
		out = append(out, slog.Any("body_summary", attrs.BodySummary))
	}
	if attrs.BodyRaw != "" {
		out = append(out, slog.String("body", attrs.BodyRaw))
	}
	if attrs.BodyB64 != "" {
		out = append(out, slog.String("body_b64", attrs.BodyB64))
	}
	if attrs.BodyTruncated {
		out = append(out, slog.Bool("body_truncated", true))
	}
	return out
}

func encodeBodyB64(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(body)
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
	logging  config.LoggingConfig
	registry *Registry
	sem      chan struct{}
	token    string
	mux      *http.ServeMux
	httpSrv  *http.Server
	connMu   sync.Mutex
	conns    map[net.Conn]http.ConnState
	oauthMgr *oauth.Manager
	anthr    *anthropic.Client
	// fb is the optional `claude -p` fallback driver. nil unless
	// cfg.Fallback.Enabled. When set, fbSem caps its concurrency
	// independently of sem.
	fb            *fallback.Client
	fbSem         chan struct{}
	httpClient    *http.Client
	ctxUsage      *contextUsageTracker
	codexContinue    *adaptercodex.ContinuationStore
	providerRegistry *adapterprovider.Registry
	codexProvider    *adaptercodex.Provider
}

// New constructs a Server from the given adapter config. The deps
// hooks come from the daemon process so the adapter reuses existing
// binary resolution and scratch dir wiring. Returns an error when
// the registry cannot be built (missing families, default model, or
// required client_identity fields); the daemon refuses to start the
// listener in that case.
func New(cfg config.AdapterConfig, logging config.LoggingConfig, deps Deps, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	if logging.Body.Mode == "" {
		logging.Body.Mode = "summary"
	}
	if logging.Body.MaxKB <= 0 {
		logging.Body.MaxKB = 32
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
		log:      log.With("subcomponent", "adapter"),
		logging:  logging,
		registry: registry,
		sem:      make(chan struct{}, max),
		token:    token,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		ctxUsage: newContextUsageTracker(),
	}
	if cfg.Codex.Enabled {
		s.codexContinue = adaptercodex.NewContinuationStore()
		s.providerRegistry = adapterprovider.NewRegistry()
		s.codexProvider = adaptercodex.NewProvider(adapterprovider.Deps{
			Config:     cfg,
			Auth:       codexAuthLookup{server: s},
			Logger:     log.With("subcomponent", "codex_provider"),
			HTTPClient: s.httpClient,
		}, adaptercodex.ProviderOptions{
			BodyLog: adaptercodex.BodyLogConfig{Mode: logging.Body.Mode, MaxKB: logging.Body.MaxKB},
		})
		s.providerRegistry.Register(s.codexProvider)
		log.LogAttrs(context.Background(), slog.LevelInfo, "adapter.provider_registry.registered",
			slog.String("provider", string(adapterresolver.ProviderCodex)),
			slog.Int("registered_count", len(s.providerRegistry.IDs())),
		)
	}
	if cfg.DirectOAuth {
		s.oauthMgr = oauth.NewManager(cfg.OAuth, "")
		id := cfg.ClientIdentity
		s.anthr = anthropic.New(nil, s.oauthMgr, anthropic.Config{
			MessagesURL:             cfg.OAuth.MessagesURL,
			OAuthAnthropicVersion:   cfg.OAuth.AnthropicVersion,
			BetaHeader:              id.BetaHeader,
			UserAgent:               id.UserAgent,
			SystemPromptPrefix:      id.SystemPromptPrefix,
			StainlessPackageVersion: id.StainlessPackageVersion,
			StainlessRuntime:        id.StainlessRuntime,
			StainlessRuntimeVersion: id.StainlessRuntimeVersion,
			CCVersion:               id.CCVersion,
			CCEntrypoint:            id.CCEntrypoint,
		})
		s.log.LogAttrs(context.Background(), slog.LevelInfo, "adapter.oauth.enabled",
			slog.Int("max_concurrent", max),
		)
	}
	if cfg.Fallback.Enabled {
		fbCfg, err := fallback.FromAdapterConfig(cfg.Fallback, deps.ResolveClaude, deps.ScratchDir)
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

func resolveCodexAuthFile(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "~/.codex/auth.json"
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func (s *Server) codexBaseURL() string {
	u := strings.TrimSpace(s.cfg.Codex.BaseURL)
	if u == "" {
		return defaultCodexBaseURL
	}
	return u
}

func (s *Server) codexWebsocketEnabled() bool {
	return s.cfg.Codex.WebsocketEnabled
}

func (s *Server) codexWebsocketURL() string {
	base := strings.TrimSpace(s.codexBaseURL())
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://")
	}
	if strings.HasPrefix(base, "http://") {
		return "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base
}

type codexAuthFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		AccountID   string `json:"account_id"`
		AccessToken string `json:"access_token"`
	} `json:"tokens"`
}

func (s *Server) readCodexAuthFile() (codexAuthFile, error) {
	p := resolveCodexAuthFile(s.cfg.Codex.AuthFile)
	data, err := os.ReadFile(p)
	if err != nil {
		return codexAuthFile{}, fmt.Errorf("read codex auth file: %w", err)
	}
	var doc codexAuthFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return codexAuthFile{}, fmt.Errorf("parse codex auth file: %w", err)
	}
	return doc, nil
}

func (s *Server) readCodexAccessToken() (string, error) {
	doc, err := s.readCodexAuthFile()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.Tokens.AccessToken) == "" {
		return "", errors.New("codex auth file missing tokens.access_token")
	}
	return doc.Tokens.AccessToken, nil
}

// codexAuthLookup adapts the Server's existing auth-file reader to
// the provider.AuthLookup interface so the Codex Provider can ask for
// a fresh token without depending on the daemon's internals.
type codexAuthLookup struct {
	server *Server
}

func (a codexAuthLookup) Token(_ context.Context) (string, error) {
	if a.server == nil {
		return "", errors.New("codex auth lookup: nil server")
	}
	return a.server.readCodexAccessToken()
}

func (s *Server) readCodexAccountID() string {
	doc, err := s.readCodexAuthFile()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(doc.Tokens.AccountID)
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

func writeModelResolutionError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: ErrorBody{
		Message: msg,
		Type:    "invalid_request_error",
		Code:    "model_not_found",
		Param:   "model",
	}})
}
