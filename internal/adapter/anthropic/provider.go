package anthropic

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
)

type requestIDContextKey struct{}

// WithRequestID binds the adapter-generated request id into ctx so the
// provider can preserve the existing response IDs and log correlation
// fields while moving collect execution off the legacy root dispatcher.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(requestID) == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func requestIDFromContext(ctx context.Context, fallback string) string {
	if ctx != nil {
		if value, ok := ctx.Value(requestIDContextKey{}).(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(fallback)
}

// ExecuteError preserves the legacy collect-path HTTP status/code
// decisions for pre-wire failures now that Provider.Execute no longer
// writes directly to the response writer.
type ExecuteError struct {
	Status  int
	Code    string
	Message string
	Cause   error
}

func (e *ExecuteError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "anthropic provider execution failed"
}

func (e *ExecuteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Provider is the Anthropic direct-OAuth implementation of the
// upstream-agnostic adapter/provider.Provider contract.
//
// Sitting 1 of Plan 4 only moves non-streaming Collect requests onto
// Provider.Execute. Streaming still returns ErrLegacyDispatchPath so the
// legacy dispatcher owns the SSE path until Sitting 2.
type Provider struct {
	log     *slog.Logger
	collect func(context.Context, adapterresolver.ResolvedRequest, string, adapterprovider.EventWriter) (adapterprovider.Result, error)
	stream  func(context.Context, adapterresolver.ResolvedRequest, string, adapterprovider.EventWriter) (adapterprovider.Result, error)
}

// ProviderOptions binds the construction-time Anthropic-only
// dependencies that the legacy root dispatcher used to pass per call.
type ProviderOptions struct {
	Collect func(context.Context, adapterresolver.ResolvedRequest, string, adapterprovider.EventWriter) (adapterprovider.Result, error)
	Stream  func(context.Context, adapterresolver.ResolvedRequest, string, adapterprovider.EventWriter) (adapterprovider.Result, error)
}

func NewProvider(deps adapterprovider.Deps, opts ProviderOptions) *Provider {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Provider{
		log:     log.With("component", "adapter", "subcomponent", "anthropic_provider"),
		collect: opts.Collect,
		stream:  opts.Stream,
	}
}

// ID matches the resolver.ProviderID for the Anthropic backend.
func (p *Provider) ID() adapterresolver.ProviderID {
	return adapterresolver.ProviderAnthropic
}

// ErrLegacyDispatchPath signals the dispatcher to use the existing
// anthropicbackend.Dispatch chain instead of the Provider.Execute
// path. Removed when the internal Anthropic rewrite (Plan 4 +
// Plan 6) lands.
var ErrLegacyDispatchPath = errors.New("anthropic provider: use legacy dispatch path")

func (p *Provider) Execute(ctx context.Context, req adapterresolver.ResolvedRequest, w adapterprovider.EventWriter) (adapterprovider.Result, error) {
	if req.OpenAI.Stream {
		if p == nil || p.stream == nil {
			return adapterprovider.Result{}, ErrLegacyDispatchPath
		}
		reqID := requestIDFromContext(ctx, req.Cursor.RequestID)
		return p.stream(ctx, req, reqID, w)
	}
	if p == nil || p.collect == nil {
		return adapterprovider.Result{}, &ExecuteError{
			Status:  http.StatusInternalServerError,
			Code:    "oauth_unconfigured",
			Message: "adapter built without anthropic collect provider; set adapter.direct_oauth=true and restart",
		}
	}
	reqID := requestIDFromContext(ctx, req.Cursor.RequestID)
	return p.collect(ctx, req, reqID, w)
}
