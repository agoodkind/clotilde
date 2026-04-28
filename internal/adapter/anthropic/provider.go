package anthropic

import (
	"context"
	"errors"
	"log/slog"

	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
)

// Provider is the registry-facing wrapper around the Anthropic OAuth
// dispatch chain. It satisfies the upstream-agnostic
// adapter/provider.Provider interface so the dispatcher can look it
// up via ResolvedRequest.Provider symmetrically with codex.Provider.
//
// This is the FIRST step of Plan 4 of the adapter refactor: get
// Anthropic into the provider registry so the dispatcher does not
// have to special-case Anthropic with a `BackendAnthropic` switch
// arm. The internal rewrite (event normalization through EventWriter,
// deletion of `anthropic/fallback/`, deletion of `anthropic_bridge.go`)
// is a follow-up slice. Until those land, Execute returns a
// `ErrLegacyDispatchPath` sentinel that the dispatcher recognizes
// and falls through to the existing `anthropicbackend.Dispatch`
// chain. The registry presence is what unlocks future symmetric
// changes.
type Provider struct {
	log *slog.Logger
}

// ProviderOptions configures the wrapper. Only logger today.
type ProviderOptions struct {
	Log *slog.Logger
}

// NewProvider returns a Provider satisfying the adapter/provider
// contract. The wrapper does not retain any anthropic.Client or oauth
// state; routing still goes through the existing Server methods until
// the internal migration completes.
func NewProvider(opts ProviderOptions) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Provider{log: log.With("component", "adapter", "subcomponent", "anthropic_provider")}
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

// Execute is the registry-facing entrypoint. Today it returns
// ErrLegacyDispatchPath because event normalization still lives in
// the existing dispatch chain. The Provider exists in the registry
// so symmetry-aware code paths can look it up.
func (p *Provider) Execute(ctx context.Context, req adapterresolver.ResolvedRequest, w adapterprovider.EventWriter) (adapterprovider.Result, error) {
	_ = ctx
	_ = req
	_ = w
	return adapterprovider.Result{}, ErrLegacyDispatchPath
}
