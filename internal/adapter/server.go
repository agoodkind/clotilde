package adapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
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

// Start binds the TCP listener and serves until ctx is done.
func (s *Server) Start(ctx context.Context) error {
	addr := s.Addr()
	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("adapter listen %s: %w", addr, err)
	}
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "adapter listening",
		slog.String("addr", addr),
		slog.Int("models", len(s.registry.List())),
	)
	errCh := make(chan error, 1)
	go func() { errCh <- s.httpSrv.Serve(lis) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)
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

type preflightError struct {
	code int
	body ErrorBody
}

func toolChoiceRequestsTools(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str != "" && str != "none"
	}
	return true
}

// preflightChat enforces adapter capability gates after alias resolution.
// It may mutate req when logprobs policy is "drop".
func (s *Server) preflightChat(req *ChatRequest, model ResolvedModel, reqID string) *preflightError {
	for msgIdx := range req.Messages {
		parts, _ := NormalizeContent(req.Messages[msgIdx].Content)
		for _, p := range parts {
			if p.Type != "input_audio" {
				continue
			}
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.audio_rejected",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.Int("message_index", msgIdx),
			)
			return &preflightError{
				code: http.StatusBadRequest,
				body: ErrorBody{
					Message: "audio content parts are not supported by this adapter",
					Type:    "invalid_request_error",
					Code:    "audio_unsupported",
				},
			}
		}
	}

	if model.Backend != BackendShunt {
		hasImage := false
		for _, msg := range req.Messages {
			parts, _ := NormalizeContent(msg.Content)
			for _, p := range parts {
				if p.Type == "image_url" {
					hasImage = true
					break
				}
			}
			if hasImage {
				break
			}
		}
		if hasImage {
			if model.Backend == BackendAnthropic && !model.SupportsVision {
				s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.vision_rejected",
					slog.String("request_id", reqID),
					slog.String("model", req.Model),
				)
				return &preflightError{
					code: http.StatusBadRequest,
					body: ErrorBody{
						Message: fmt.Sprintf("model %q does not support vision input", req.Model),
						Type:    "invalid_request_error",
						Code:    "unsupported_content",
					},
				}
			}
			if model.Backend == BackendFallback {
				s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.vision_rejected",
					slog.String("request_id", reqID),
					slog.String("model", req.Model),
				)
				return &preflightError{
					code: http.StatusBadRequest,
					body: ErrorBody{
						Message: "vision input is not supported on the fallback backend",
						Type:    "invalid_request_error",
						Code:    "fallback_no_vision",
					},
				}
			}
		}
	}

	for tIdx, t := range req.Tools {
		if t.Function.Name == "" {
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.tools_invalid_name",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.Int("tool_index", tIdx),
				slog.String("reason", "empty function.name"),
			)
			return &preflightError{
				code: http.StatusBadRequest,
				body: ErrorBody{
					Message: fmt.Sprintf("tools[%d].function.name is required and must be non-empty", tIdx),
					Type:    "invalid_request_error",
					Code:    "invalid_tool_name",
				},
			}
		}
	}
	for fIdx, f := range req.Functions {
		if f.Name == "" {
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.tools_invalid_name",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.Int("function_index", fIdx),
				slog.String("reason", "empty functions[].name"),
			)
			return &preflightError{
				code: http.StatusBadRequest,
				body: ErrorBody{
					Message: fmt.Sprintf("functions[%d].name is required and must be non-empty", fIdx),
					Type:    "invalid_request_error",
					Code:    "invalid_tool_name",
				},
			}
		}
	}

	if model.Backend == BackendAnthropic {
		wantsTools := len(req.Tools) > 0 || len(req.Functions) > 0 || toolChoiceRequestsTools(req.ToolChoice)
		if wantsTools && !model.SupportsTools {
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.tools_rejected",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
			)
			return &preflightError{
				code: http.StatusBadRequest,
				body: ErrorBody{
					Message: "tools are not enabled for this model alias",
					Type:    "invalid_request_error",
					Code:    "unsupported_content",
				},
			}
		}
	}

	wantsLogprobs := (req.Logprobs != nil && *req.Logprobs) || req.TopLogprobs != nil
	if !wantsLogprobs {
		return nil
	}
	switch model.Backend {
	case BackendAnthropic:
		switch s.logprobs.Anthropic {
		case "reject":
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.logprobs_rejected",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.String("backend", "anthropic"),
			)
			return &preflightError{
				code: http.StatusBadRequest,
				body: ErrorBody{
					Message: "logprobs are not supported for this backend",
					Type:    "invalid_request_error",
					Code:    "unsupported_param",
				},
			}
		case "drop":
			req.Logprobs = nil
			req.TopLogprobs = nil
		}
	case BackendFallback:
		switch s.logprobs.Fallback {
		case "reject":
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.preflight.logprobs_rejected",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.String("backend", "fallback"),
			)
			return &preflightError{
				code: http.StatusBadRequest,
				body: ErrorBody{
					Message: "logprobs are not supported for this backend",
					Type:    "invalid_request_error",
					Code:    "unsupported_param",
				},
			}
		case "drop":
			req.Logprobs = nil
			req.TopLogprobs = nil
		}
	}
	return nil
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	entries := s.registry.List()
	resp := ModelsResponse{Object: "list"}
	for _, m := range entries {
		resp.Data = append(resp.Data, ModelEntry{
			ID:          m.Alias,
			Object:      "model",
			OwnedBy:     "clyde",
			Context:     m.Context,
			Efforts:     m.Efforts,
			Backend:     m.Backend,
			ClaudeModel: m.ClaudeModel,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	reqID := newRequestID()
	w.Header().Set("x-clyde-request-id", reqID)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "failed to read body")
		return
	}
	bodyBytes := len(body)
	logBody := body
	bodyTruncated := false
	if bodyBytes > rawChatBodyLimit {
		logBody = body[:rawChatBodyLimit]
		bodyTruncated = true
	}
	rawAttrs := rawChatLogEvent{
		RequestID:     reqID,
		Method:        r.Method,
		Path:          r.URL.Path,
		RemoteAddr:    r.RemoteAddr,
		Headers:       redactedHeaders(r.Header),
		BodyBytes:     bodyBytes,
		Body:          string(logBody),
		BodyTruncated: bodyTruncated,
	}
	s.log.LogAttrs(r.Context(), slog.LevelDebug, "adapter.chat.raw", rawAttrs.asAttrs()...)

	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "messages is required")
		return
	}

	model, effort, err := s.registry.Resolve(req.Model, req.ReasoningEffort)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown_model", err.Error())
		return
	}

	toolNames := make([]string, 0, len(req.Tools)+len(req.Functions))
	for _, t := range req.Tools {
		toolNames = append(toolNames, t.Function.Name)
	}
	for _, f := range req.Functions {
		toolNames = append(toolNames, f.Name)
	}
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.received",
		slog.String("request_id", reqID),
		slog.String("alias", req.Model),
		slog.String("backend", string(model.Backend)),
		slog.Int("message_count", len(req.Messages)),
		slog.Int("tool_count", len(req.Tools)+len(req.Functions)),
		slog.Any("tool_names", toolNames),
		slog.Bool("stream", req.Stream),
	)

	if perr := s.preflightChat(&req, model, reqID); perr != nil {
		writeJSON(w, perr.code, ErrorResponse{Error: perr.body})
		return
	}

	if model.Backend == BackendShunt {
		s.forwardShunt(w, r, model, body)
		return
	}

	if model.Backend == BackendFallback {
		// Explicit-mode dispatch: alias is bound to the fallback
		// backend directly, no OAuth attempt is made.
		_ = s.handleFallback(w, r, req, model, reqID, false)
		return
	}

	if model.Backend == BackendAnthropic {
		s.dispatchAnthropicWithFallback(w, r, req, model, effort, reqID, body)
		return
	}

	if err := s.acquire(r.Context()); err != nil {
		writeError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
		return
	}
	defer s.release()

	system, prompt := BuildPrompt(req.Messages)
	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	if instr := jsonSpec.SystemPrompt(false); instr != "" {
		if system == "" {
			system = instr
		} else {
			system = system + "\n\n" + instr
		}
	}
	runner := NewRunner(s.deps, model, effort, system, prompt, reqID)
	started := time.Now()
	stdout, cancel, err := runner.Spawn(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "spawn_failed", err.Error())
		return
	}
	defer cancel()

	if req.Stream {
		// Streaming JSON enforcement is impractical because chunks
		// arrive token-by-token and cannot be re-issued mid-stream.
		// The system prompt already nudges claude toward raw JSON;
		// pure structured-output clients (humanify, etc.) almost
		// always use the non-streaming path.
		s.streamChat(w, r, req, model, stdout, reqID, started)
		return
	}
	s.collectChat(w, req, model, stdout, reqID, started, jsonSpec)
}

func (s *Server) collectChat(w http.ResponseWriter, req ChatRequest, model ResolvedModel, stdout io.ReadCloser, reqID string, started time.Time, jsonSpec JSONResponseSpec) {
	text, usage, err := CollectStream(stdout)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}
	finalText := text
	jsonRetried := false
	if jsonSpec.Mode != "" {
		coerced := CoerceJSON(text)
		if !LooksLikeJSON(coerced) {
			// One-shot retry with stronger instruction.
			jsonRetried = true
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "structured-output parse failed; retrying",
				slog.String("request_id", reqID),
				slog.Int("first_attempt_bytes", len(text)),
			)
			retrySystem, retryPrompt := BuildPrompt(req.Messages)
			retrySystem = strings.TrimSpace(retrySystem + "\n\n" + jsonSpec.SystemPrompt(true))
			retryRunner := NewRunner(s.deps, model, "", retrySystem, retryPrompt, reqID+"-r")
			retryStdout, retryCancel, spawnErr := retryRunner.Spawn(r2Context(req))
			if spawnErr == nil {
				defer retryCancel()
				retryText, retryUsage, collectErr := CollectStream(retryStdout)
				if collectErr == nil {
					retryCoerced := CoerceJSON(retryText)
					if LooksLikeJSON(retryCoerced) {
						finalText = retryCoerced
						usage.PromptTokens += retryUsage.PromptTokens
						usage.CompletionTokens += retryUsage.CompletionTokens
						usage.TotalTokens += retryUsage.TotalTokens
					} else {
						// Surface the second attempt regardless so the
						// caller can see what went wrong; humanify will
						// throw, which is the right signal for them.
						finalText = retryText
					}
				}
			}
		} else {
			finalText = coerced
		}
	}
	resp := ChatResponse{
		ID:                reqID,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             model.Alias,
		SystemFingerprint: systemFingerprint,
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:    "assistant",
				Content: json.RawMessage(strconv.Quote(finalText)),
			},
			FinishReason: "stop",
		}},
		Usage: &usage,
	}
	writeJSON(w, http.StatusOK, resp)
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "chat completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", false),
		slog.String("json_mode", jsonSpec.Mode),
		slog.Bool("json_retried", jsonRetried),
	)
}

// r2Context returns a fresh background context for the retry spawn.
// The original request context is unavailable here without a refactor;
// using context.Background lets the retry complete even if the caller
// has already disconnected, but most clients keep the connection open
// while waiting for the JSON reply so this is fine in practice.
func r2Context(_ ChatRequest) context.Context {
	return context.Background()
}

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, stdout io.ReadCloser, reqID string, started time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_flusher", "streaming not supported by this transport")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	sink := func(chunk StreamChunk) error {
		chunk.SystemFingerprint = systemFingerprint
		b, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	usage, err := TranslateStream(stdout, model.Alias, reqID, sink)
	if err != nil {
		s.log.LogAttrs(context.Background(), slog.LevelWarn, "stream translate error",
			slog.String("request_id", reqID),
			slog.Any("err", err),
		)
	}
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		_ = sink(StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model.Alias,
			Choices: []StreamChoice{},
			Usage:   &usage,
		})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()

	s.log.LogAttrs(context.Background(), slog.LevelInfo, "chat completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", true),
	)
}

func (s *Server) handleLegacy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var legacy struct {
		Model           string `json:"model"`
		Prompt          string `json:"prompt"`
		Stream          bool   `json:"stream,omitempty"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&legacy); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	synthetic := ChatRequest{
		Model:           legacy.Model,
		Stream:          legacy.Stream,
		ReasoningEffort: legacy.ReasoningEffort,
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(strconv.Quote(legacy.Prompt)),
		}},
	}
	body, _ := json.Marshal(synthetic)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")
	s.handleChat(w, r)
}

func (s *Server) forwardShunt(w http.ResponseWriter, r *http.Request, model ResolvedModel, body []byte) {
	shunt, ok := s.registry.Shunt(model.Shunt)
	if !ok || shunt.BaseURL == "" {
		writeError(w, http.StatusNotImplemented, "shunt_unconfigured",
			"alias routes to shunt "+model.Shunt+" but no base URL is configured")
		return
	}
	apiKey := shunt.APIKey
	if apiKey == "" && shunt.APIKeyEnv != "" {
		apiKey = os.Getenv(shunt.APIKeyEnv)
	}

	// Mutate the request body if we need to. Two reasons:
	//   1. shunt.Model overrides the alias the caller sent.
	//   2. response_format json_schema does not work on most local
	//      backends (LM Studio, Ollama, etc.) so we strip it and
	//      prepend a system message that tells the model to emit
	//      raw JSON. The clyde adapter then post-validates and
	//      retries once if the reply does not parse.
	var rawReq map[string]any
	jsonSpec := JSONResponseSpec{}
	if err := json.Unmarshal(body, &rawReq); err == nil {
		if shunt.Model != "" {
			rawReq["model"] = shunt.Model
		}
		if rf, ok := rawReq["response_format"]; ok {
			rfBytes, _ := json.Marshal(rf)
			jsonSpec = ParseResponseFormat(rfBytes)
		}
		if jsonSpec.Mode != "" {
			injectJSONSystemMessage(rawReq, jsonSpec.SystemPrompt(false))
			delete(rawReq, "response_format")
		}
		body, _ = json.Marshal(rawReq)
	}

	respBody, status, hdr, err := shuntCall(r.Context(), shunt.BaseURL, apiKey, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "shunt_dial_failed", err.Error())
		return
	}

	if jsonSpec.Mode != "" && status == http.StatusOK {
		coerced, ok := coerceShuntJSON(respBody)
		if !ok {
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "shunt structured-output parse failed; retrying",
				slog.String("model", model.Alias),
				slog.String("shunt", model.Shunt),
				slog.Int("first_attempt_bytes", len(respBody)),
			)
			injectJSONSystemMessage(rawReq, jsonSpec.SystemPrompt(true))
			body2, _ := json.Marshal(rawReq)
			rb2, st2, h2, err2 := shuntCall(r.Context(), shunt.BaseURL, apiKey, body2)
			if err2 == nil && st2 == http.StatusOK {
				if c2, ok2 := coerceShuntJSON(rb2); ok2 {
					respBody, status, hdr = c2, st2, h2
				} else {
					respBody, status, hdr = rb2, st2, h2
				}
			}
		} else {
			respBody = coerced
		}
	}

	for k, v := range hdr {
		// Drop any upstream-set Content-Length; we may have rewritten
		// the body and a stale length triggers the http2 framework to
		// return zero bytes to the client.
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
	w.WriteHeader(status)
	_, _ = w.Write(respBody)
}

// injectJSONSystemMessage prepends (or appends to existing system
// content) an instruction telling the model to emit raw JSON only.
func injectJSONSystemMessage(req map[string]any, instruction string) {
	if instruction == "" {
		return
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) > 0 {
		first, _ := msgs[0].(map[string]any)
		if first != nil {
			role, _ := first["role"].(string)
			if role == "system" || role == "developer" {
				if existing, ok := first["content"].(string); ok {
					first["content"] = instruction + "\n\n" + existing
				} else {
					first["content"] = instruction
				}
				msgs[0] = first
				req["messages"] = msgs
				return
			}
		}
	}
	sys := map[string]any{"role": "system", "content": instruction}
	req["messages"] = append([]any{sys}, msgs...)
}

// shuntCall posts body to the shunt's chat/completions endpoint and
// returns body+status+headers.
func shuntCall(ctx context.Context, baseURL, apiKey string, body []byte) ([]byte, int, http.Header, error) {
	target := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	return rb, resp.StatusCode, resp.Header, nil
}

// coerceShuntJSON walks the OpenAI-shaped response, runs CoerceJSON
// on choices[].message.content, and returns the rewritten body if all
// choices now parse as JSON.
func coerceShuntJSON(body []byte) ([]byte, bool) {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body, false
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return body, false
	}
	allOK := true
	for i, c := range choices {
		choice, _ := c.(map[string]any)
		if choice == nil {
			allOK = false
			continue
		}
		msg, _ := choice["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].(string)
		if content == "" {
			continue
		}
		coerced := CoerceJSON(content)
		if !LooksLikeJSON(coerced) {
			allOK = false
			continue
		}
		msg["content"] = coerced
		choice["message"] = msg
		choices[i] = choice
	}
	resp["choices"] = choices
	out, _ := json.Marshal(resp)
	return out, allOK
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

func redactedHeaders(input http.Header) map[string]string {
	out := make(map[string]string, len(input))
	for key, values := range input {
		normalized := strings.ToLower(key)
		if redactedHeader(normalized) {
			out[normalized] = "[redacted]"
			continue
		}
		out[normalized] = strings.Join(values, ", ")
	}
	return out
}

func redactedHeader(name string) bool {
	switch name {
	case "authorization", "proxy-authorization", "cookie", "x-clyde-token":
		return true
	}
	return strings.HasSuffix(name, "-api-key")
}

// dispatchAnthropicWithFallback runs the direct-Anthropic backend
// (Bearer auth via the OAuth keychain token) and, when the
// configured trigger covers on_oauth_failure, escalates to either
// the configured forward_to_shunt or the `claude -p` fallback.
// FailureEscalation picks whether the Anthropic or the fallback
// error surfaces when both fail.
//
// When fallback is disabled or the trigger does not cover Anthropic
// failures, the function delegates to s.handleOAuth directly with
// escalate=false (preserving the pre-fallback behavior).
func (s *Server) dispatchAnthropicWithFallback(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, body []byte) {
	fb := s.cfg.Fallback
	escalate := fb.Enabled &&
		(fb.Trigger == FallbackTriggerOnOAuthFailure || fb.Trigger == FallbackTriggerBoth)

	if !escalate {
		_ = s.handleOAuth(w, r, req, model, effort, reqID, false)
		return
	}

	anthErr := s.handleOAuth(w, r, req, model, effort, reqID, true)
	if anthErr == nil {
		return
	}

	s.log.LogAttrs(context.Background(), slog.LevelWarn, "adapter.fallback.escalating",
		slog.String("request_id", reqID),
		slog.String("alias", model.Alias),
		slog.String("anthropic_err", anthErr.Error()),
		slog.Bool("forward_to_shunt", fb.ForwardToShunt.Enabled),
	)

	if fb.ForwardToShunt.Enabled {
		shunt, ok := s.registry.Shunt(fb.ForwardToShunt.Shunt)
		if !ok || shunt.BaseURL == "" {
			s.log.LogAttrs(context.Background(), slog.LevelError, "adapter.fallback.shunt_unconfigured",
				slog.String("request_id", reqID),
				slog.String("shunt", fb.ForwardToShunt.Shunt),
			)
			s.surfaceFallbackFailure(w, anthErr, fmt.Errorf(
				"forward_to_shunt %q not configured (base_url empty)", fb.ForwardToShunt.Shunt))
			return
		}
		// Reuse the existing shunt path; ResolvedModel.Shunt is
		// the lookup key for forwardShunt.
		shuntModel := model
		shuntModel.Backend = BackendShunt
		shuntModel.Shunt = fb.ForwardToShunt.Shunt
		s.forwardShunt(w, r, shuntModel, body)
		return
	}

	if s.fb == nil {
		s.surfaceFallbackFailure(w, anthErr, fmt.Errorf("fallback client not constructed"))
		return
	}
	if model.CLIAlias == "" {
		s.surfaceFallbackFailure(w, anthErr, fmt.Errorf(
			"family %q has no [adapter.fallback.cli_aliases] entry; cannot escalate", model.FamilySlug))
		return
	}

	fbErr := s.handleFallback(w, r, req, model, reqID, true)
	if fbErr == nil {
		return
	}
	s.surfaceFallbackFailure(w, anthErr, fbErr)
}

// surfaceFallbackFailure writes the error chosen by
// FailureEscalation. Called only after both attempts have failed
// and nothing has been written to the wire yet (the escalate=true
// path returns before any header/byte commits).
func (s *Server) surfaceFallbackFailure(w http.ResponseWriter, anthErr, fbErr error) {
	switch s.cfg.Fallback.FailureEscalation {
	case FallbackEscalationOAuthError:
		writeError(w, http.StatusBadGateway, "upstream_error", anthErr.Error())
	default: // FallbackEscalationFallbackError
		writeError(w, http.StatusBadGateway, "fallback_error", fbErr.Error())
	}
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

// mapStopReason translates the Anthropic stop_reason vocabulary into
// the OpenAI finish_reason vocabulary. Empty input maps to "stop".
func mapStopReason(s string) string {
	switch s {
	case "end_turn", "stop_sequence", "":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	}
	return s
}

func writeError(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, ErrorResponse{Error: ErrorBody{Message: msg, Type: kind}})
}
