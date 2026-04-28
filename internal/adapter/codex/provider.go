package codex

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
	"goodkind.io/clyde/internal/config"
)

// Provider implements adapterprovider.Provider for the Codex
// websocket-only path. Construction binds the runtime dependencies
// once at daemon startup; Execute is the per-request entry point that
// stitches the websocket transport, the continuation ledger, and the
// normalized event emission together.
type Provider struct {
	cfg          config.AdapterCodex
	auth         adapterprovider.AuthLookup
	log          *slog.Logger
	httpClient   *http.Client
	now          func() time.Time
	continuation *ContinuationStore
	accountID    string
	bodyLog      BodyLogConfig
}

// ProviderOptions extends the generic provider.Deps with Codex-only
// settings the dispatcher knows at construction time. Today: the
// account id (lifted from auth.json by daemon startup) and the
// continuation ledger pointer.
type ProviderOptions struct {
	AccountID    string
	Continuation *ContinuationStore
	BodyLog      BodyLogConfig
}

// NewProvider constructs the Codex Provider.
func NewProvider(deps adapterprovider.Deps, opts ProviderOptions) *Provider {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	httpClient := deps.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Provider{
		cfg:          deps.Config.Codex,
		auth:         deps.Auth,
		log:          log,
		httpClient:   httpClient,
		now:          now,
		continuation: opts.Continuation,
		accountID:    strings.TrimSpace(opts.AccountID),
		bodyLog:      opts.BodyLog,
	}
}

// ID satisfies adapterprovider.Provider.
func (p *Provider) ID() adapterresolver.ProviderID { return adapterresolver.ProviderCodex }

// ErrCodexProviderNotConfigured signals that the Provider was
// constructed without the dependencies it needs to make a wire call.
// Today that means missing AuthLookup or empty BaseURL/WebsocketURL.
var ErrCodexProviderNotConfigured = errors.New("codex provider: not configured")

// Execute satisfies adapterprovider.Provider.Execute. It builds a
// DirectConfig from the provider's deps, runs the websocket transport
// via RunDirect, and surfaces the result as adapterprovider.Result.
//
// The bridge to provider.EventWriter goes via render.Event values
// derived from the StreamChunk emit closure RunDirect uses today.
// This is intentionally a thin shim: the chunk-to-event conversion
// here covers the assistant-text-delta path in full, with tool calls
// and reasoning forwarded through opaque deltas. Plan 6 (render
// finalization) replaces this shim with a direct event emit from the
// websocket parser.
func (p *Provider) Execute(ctx context.Context, req adapterresolver.ResolvedRequest, w adapterprovider.EventWriter) (adapterprovider.Result, error) {
	if p == nil {
		return adapterprovider.Result{}, ErrCodexProviderNotConfigured
	}
	if p.auth == nil {
		return adapterprovider.Result{}, adapterprovider.ErrAuthMissing
	}

	token, err := p.auth.Token(ctx)
	if err != nil {
		return adapterprovider.Result{}, fmt.Errorf("codex provider: auth lookup: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return adapterprovider.Result{}, adapterprovider.ErrAuthMissing
	}

	directCfg := DirectConfig{
		HTTPClient:       p.httpClient,
		BaseURL:          codexBaseURL(p.cfg.BaseURL),
		WebsocketEnabled: true,
		WebsocketURL:     codexWebsocketURL(p.cfg.BaseURL),
		Token:            token,
		AccountID:        p.accountID,
		RequestID:        req.Cursor.RequestID,
		Continuation:     p.continuation,
		Log:              p.log,
		BodyLog:          p.bodyLog,
	}

	model := resolvedModelFromRequest(req)
	emit := chunkToEventBridge(w)

	runResult, runErr := RunDirect(ctx, directCfg, req.OpenAI, model, req.Effort.String(), emit)
	if runErr != nil {
		return adapterprovider.Result{}, runErr
	}
	if flushErr := w.Flush(); flushErr != nil {
		return adapterprovider.Result{}, flushErr
	}
	return adapterprovider.Result{
		Usage:                      runResult.Usage,
		FinishReason:               runResult.FinishReason,
		DerivedCacheCreationTokens: runResult.DerivedCacheCreationTokens,
	}, nil
}

// resolvedModelFromRequest reconstructs the legacy
// adaptermodel.ResolvedModel surface from the typed ResolvedRequest.
// The websocket transport still consumes ResolvedModel today; Plan 7
// removes that dependency.
func resolvedModelFromRequest(req adapterresolver.ResolvedRequest) adaptermodel.ResolvedModel {
	return adaptermodel.ResolvedModel{
		Alias:           req.Model,
		Backend:         adaptermodel.BackendCodex,
		ClaudeModel:     req.Model,
		Context:         req.ContextBudget.InputTokens,
		Effort:          req.Effort.String(),
		MaxOutputTokens: req.ContextBudget.OutputTokens,
		FamilySlug:      req.Family,
	}
}

// chunkToEventBridge produces the StreamChunk emit closure used by
// the existing transport. Each chunk maps to a single render.Event
// with kind EventAssistantTextDelta carrying the chunk's content
// payload. The bridge intentionally does not interpret tool call or
// reasoning deltas here; downstream readers see them as opaque text
// deltas until Plan 6 adds a typed parser.
func chunkToEventBridge(w adapterprovider.EventWriter) func(adapteropenai.StreamChunk) error {
	return func(chunk adapteropenai.StreamChunk) error {
		text := chunkPrimaryText(chunk)
		if text == "" {
			return nil
		}
		return w.WriteEvent(adapterrender.Event{
			Kind: adapterrender.EventAssistantTextDelta,
			Text: text,
		})
	}
}

// chunkPrimaryText pulls the most useful text payload from a stream
// chunk. The full StreamChunk shape carries delta content, role, and
// tool calls; this helper covers the text-delta path. Other shapes
// fall through to empty so the bridge stays a no-op rather than
// emitting a malformed event.
func chunkPrimaryText(chunk adapteropenai.StreamChunk) string {
	for _, choice := range chunk.Choices {
		if text := strings.TrimSpace(choice.Delta.Content); text != "" {
			return text
		}
	}
	return ""
}

// codexBaseURL applies the documented default for the Codex Responses
// HTTP endpoint when the config leaves BaseURL empty.
func codexBaseURL(raw string) string {
	if v := strings.TrimSpace(raw); v != "" {
		return v
	}
	return "https://chatgpt.com/backend-api/codex/responses"
}

// codexWebsocketURL converts a Codex base URL into the matching
// websocket URL. https:// becomes wss://; http:// becomes ws://; any
// other scheme passes through.
func codexWebsocketURL(raw string) string {
	base := codexBaseURL(raw)
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	}
	return base
}
