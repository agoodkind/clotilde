package adapter

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

// codexInputContent and codexInputItem preserve the legacy root-side
// representation of a Codex Responses request body. Live transport
// flows now build the backend-local
// `internal/adapter/codex.HTTPTransportRequest` shape directly via
// `adaptercodex.BuildRequest`. The root types remain so that existing
// adapter-level tests can keep inspecting per-content-part fields
// (`type`, `text`, etc.) until the test files are relocated alongside
// the backend implementation.
type codexInputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type codexInputItem map[string]any

type codexRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include,omitempty"`
	PromptCache       string            `json:"prompt_cache_key,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	ClientMetadata    map[string]string `json:"client_metadata,omitempty"`
	Reasoning         *codexReasoning   `json:"reasoning,omitempty"`
	MaxCompletion     *int              `json:"max_completion_tokens,omitempty"`
	Input             []codexInputItem  `json:"input"`
	Tools             []any             `json:"tools,omitempty"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls,omitempty"`
}

type codexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type codexRunResult = adaptercodex.RunResult

// codexNow / codexGetwd / codexShellName are root-side test seams.
// They are wired through to the backend-local `adaptercodex.NowFunc`,
// `adaptercodex.GetwdFn`, and `adaptercodex.ShellNameFn` hooks at
// init() so the backend builder uses the same environment.
var (
	codexNow       = time.Now
	codexGetwd     = os.Getwd
	codexShellName = func() string {
		shell := strings.TrimSpace(os.Getenv("SHELL"))
		if shell == "" {
			return "sh"
		}
		parts := strings.Split(shell, "/")
		return parts[len(parts)-1]
	}
)

func init() {
	adaptercodex.NowFunc = func() time.Time { return codexNow() }
	adaptercodex.GetwdFn = func() (string, error) { return codexGetwd() }
	adaptercodex.ShellNameFn = func() string { return codexShellName() }
}

func sanitizeForUpstreamCache(text string) string { return adaptercodex.SanitizeForUpstreamCache(text) }

func codexMapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func codexRawString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func codexItemType(item map[string]any) string   { return codexMapString(item, "type") }
func codexItemStatus(item map[string]any) string { return codexMapString(item, "status") }

// buildCodexRequest is a root-side compatibility wrapper that reshapes
// the backend-local `adaptercodex.BuildRequest` result into the legacy
// `codexRequest`/`codexInputItem` form expected by the older root
// tests. The live transport path in `runCodexDirect` calls
// `adaptercodex.BuildRequest` directly and never touches this wrapper.
func buildCodexRequest(req ChatRequest, model ResolvedModel, effort string) codexRequest {
	payload := adaptercodex.BuildRequest(req, model, effort)
	out := codexRequest{
		Model:             payload.Model,
		Instructions:      payload.Instructions,
		Store:             payload.Store,
		Stream:            payload.Stream,
		Include:           payload.Include,
		PromptCache:       payload.PromptCache,
		ServiceTier:       payload.ServiceTier,
		ClientMetadata:    payload.ClientMetadata,
		Reasoning:         (*codexReasoning)(payload.Reasoning),
		MaxCompletion:     payload.MaxCompletion,
		Input:             make([]codexInputItem, 0, len(payload.Input)),
		Tools:             payload.Tools,
		ToolChoice:        payload.ToolChoice,
		ParallelToolCalls: payload.ParallelToolCalls,
	}
	for _, item := range payload.Input {
		legacy := codexInputItem{}
		for key, value := range item {
			if key == "content" {
				parts, _ := value.([]map[string]any)
				content := make([]codexInputContent, 0, len(parts))
				for _, part := range parts {
					content = append(content, codexInputContent{
						Type:     codexMapString(part, "type"),
						Text:     codexRawString(part, "text"),
						ImageURL: codexRawString(part, "image_url"),
						Detail:   codexRawString(part, "detail"),
					})
				}
				legacy[key] = content
				continue
			}
			legacy[key] = value
		}
		out.Input = append(out.Input, legacy)
	}
	return out
}

func codexClientMetadata(installationID, windowID string) map[string]string {
	return adaptercodex.ClientMetadata(installationID, windowID)
}

func effectiveCodexReasoning(req ChatRequest, effort string) *codexReasoning {
	return (*codexReasoning)(adaptercodex.EffectiveReasoning(req, effort))
}

func effectiveCodexAppEffort(req ChatRequest) any  { return adaptercodex.EffectiveAppEffort(req) }
func effectiveCodexAppSummary(req ChatRequest) any { return adaptercodex.EffectiveAppSummary(req) }

func parseCodexSSE(body io.Reader, renderer *tooltrans.EventRenderer, emit func(tooltrans.OpenAIStreamChunk) error) (codexRunResult, error) {
	return adaptercodex.ParseSSE(body, renderer, emit)
}

// runCodexDirect builds the Codex transport request through the
// backend-local `adaptercodex.BuildRequest` entrypoint, then dispatches
// it through the websocket-or-HTTP selector that the Codex package
// owns. The root facade keeps only auth/account plumbing.
func (s *Server) runCodexDirect(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	token, err := s.readCodexAccessToken()
	if err != nil {
		return adaptercodex.NewRunResult("stop"), err
	}
	transportPayload := adaptercodex.BuildRequest(req, model, effort)
	if s.codexWebsocketEnabled() {
		wsReq := adaptercodex.ResponseCreateRequestFromHTTP(transportPayload)
		res, wsErr := adaptercodex.RunWebsocketTransport(ctx, adaptercodex.WebsocketTransportConfig{
			URL:       s.codexWebsocketURL(),
			Token:     token,
			RequestID: reqID,
			Alias:     model.Alias,
		}, wsReq, emit)
		if wsErr == nil {
			return res, nil
		}
		if !errors.Is(wsErr, adaptercodex.ErrWebsocketFallbackToHTTP) {
			return adaptercodex.NewRunResult("stop"), wsErr
		}
	}
	return adaptercodex.RunHTTPTransport(ctx, s.httpClient, adaptercodex.HTTPTransportConfig{
		BaseURL:        s.codexBaseURL(),
		Token:          token,
		AccountID:      s.readCodexAccountID(),
		RequestID:      reqID,
		Alias:          model.Alias,
		ConversationID: strings.TrimSpace(transportPayload.PromptCache),
	}, transportPayload, emit)
}

func (s *Server) collectCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	return adaptercodex.Collect(s, w, r, req, model, effort, reqID, started)
}

func (s *Server) streamCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	return adaptercodex.Stream(s, w, r, req, model, effort, reqID, started)
}

func (s *Server) dispatchCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string) {
	started := time.Now()
	if req.Stream {
		if err := s.streamCodex(w, r, req, model, effort, reqID, started); err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	}
	if err := s.collectCodex(w, r, req, model, effort, reqID, started); err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
	}
}

// Compile-time references to keep slog usable in this trimmed file.
var _ = slog.LevelDebug
