package resolver

import (
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
)

// ModelRegistryAdapter wraps the existing model.Registry so it satisfies
// the resolver's ModelRegistry interface. It is the production binding
// between the resolver and the existing per-alias resolution logic.
//
// The adapter performs no IO. It calls model.Registry.Resolve and
// projects the returned model.ResolvedModel into the resolver's typed
// ResolvedModelView. Only the fields the resolver needs are copied;
// everything else stays on the underlying ResolvedModel and is
// available via downstream code paths that still consume the existing
// type.
type ModelRegistryAdapter struct {
	inner *adaptermodel.Registry
}

// NewModelRegistryAdapter binds an existing model.Registry to the
// resolver interface. A nil inner is allowed at construction so call
// sites can wire the adapter early; Resolve returns a typed error if
// invoked while the inner registry is nil.
func NewModelRegistryAdapter(inner *adaptermodel.Registry) *ModelRegistryAdapter {
	return &ModelRegistryAdapter{inner: inner}
}

// Resolve satisfies the ModelRegistry interface. It calls
// model.Registry.Resolve and projects the result.
func (a *ModelRegistryAdapter) Resolve(alias, reqEffort string) (ResolvedModelView, error) {
	if a == nil || a.inner == nil {
		return ResolvedModelView{}, ErrUnresolvedProvider
	}
	resolved, effort, err := a.inner.Resolve(alias, reqEffort)
	if err != nil {
		return ResolvedModelView{}, err
	}
	provider := backendToProvider(resolved.Backend)
	parsedEffort, _ := ParseEffort(effort)
	family := resolved.FamilySlug
	if family == "" {
		family = resolved.ClaudeModel
	}
	return ResolvedModelView{
		Provider:        provider,
		Family:          family,
		Model:           resolved.ClaudeModel,
		Effort:          parsedEffort,
		Context:         resolved.Context,
		MaxOutputTokens: resolved.MaxOutputTokens,
	}, nil
}

// backendToProvider maps the existing model.Backend* constants to the
// resolver's narrower ProviderID enum. Backends that the resolver does
// not represent (claude, shunt, fallback) map to ProviderUnknown so
// the dispatcher can route them through their legacy paths.
func backendToProvider(backend string) ProviderID {
	switch backend {
	case adaptermodel.BackendAnthropic:
		return ProviderAnthropic
	case adaptermodel.BackendCodex:
		return ProviderCodex
	}
	return ProviderUnknown
}
