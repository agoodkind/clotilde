package resolver

import (
	"errors"

	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
)

// ModelRegistry is the narrow interface the resolver consumes from the
// adapter's existing model registry. It exists so the resolver can be
// tested in isolation without dragging the full Registry implementation
// into every test.
//
// The single method mirrors model.Registry.Resolve. It accepts a model
// alias and a wire-form effort string (which may be empty) and returns
// the resolved model fields the resolver lifts into its typed view.
type ModelRegistry interface {
	Resolve(alias, reqEffort string) (ResolvedModelView, error)
}

// ResolvedModelView is the resolver's view of the existing model
// registry's per-alias output. It is a typed surface, not a pointer to
// the existing model.ResolvedModel, so the resolver can be exercised
// with fakes that do not depend on the live registry.
type ResolvedModelView struct {
	Provider        ProviderID
	Family          string
	Model           string
	Effort          Effort
	Context         int
	MaxOutputTokens int
}

// ErrUnresolvedProvider signals that the model alias resolved to a
// backend the resolver does not support (today: shunt or fallback).
// The dispatcher must fall back to its legacy path or reject the
// request explicitly.
var ErrUnresolvedProvider = errors.New("resolver: alias does not map to a known provider")

// Resolve consumes a typed cursor.Request and a ModelRegistry and
// returns a ResolvedRequest. It is a pure function in the sense that
// it does not perform IO; the registry is consulted in-memory.
//
// This is the Step A skeleton. The full implementation lands in Step C
// once the existing model registry exposes the ModelRegistry interface
// or the resolver gains a small adapter.
func Resolve(req adaptercursor.Request, registry ModelRegistry) (ResolvedRequest, error) {
	if registry == nil {
		return ResolvedRequest{}, errors.New("resolver: nil registry")
	}
	view, err := registry.Resolve(req.OpenAI.Model, req.OpenAI.ReasoningEffort)
	if err != nil {
		return ResolvedRequest{}, err
	}
	if !view.Provider.Valid() {
		return ResolvedRequest{}, ErrUnresolvedProvider
	}
	return ResolvedRequest{
		Provider: view.Provider,
		Family:   view.Family,
		Model:    view.Model,
		Effort:   view.Effort,
		ContextBudget: ContextBudget{
			InputTokens:  view.Context,
			OutputTokens: view.MaxOutputTokens,
			TotalTokens:  view.Context,
		},
		Cursor: req,
		OpenAI: req.OpenAI,
	}, nil
}
