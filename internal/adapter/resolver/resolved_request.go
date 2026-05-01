package resolver

import (
	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	"goodkind.io/clyde/internal/correlation"
)

// ContextBudget describes the advertised token budget for a resolved
// request. All three fields are informational. Upstream enforces the
// real limit. Zero values mean the family did not declare a budget;
// the provider then relies on upstream defaults.
type ContextBudget struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// ResolvedRequest is the typed object that flows from the resolver
// into the dispatcher and on to the per-provider implementations. It
// carries:
//
//   - the Cursor product translation (Cursor) so providers can read
//     the conversation key, request id, and tool-presence flags;
//   - the decoded OpenAI wire request (OpenAI) so providers can build
//     their upstream request without re-decoding;
//   - the resolved model identity (Provider, Family, Model, Effort,
//     Verbosity, ContextBudget) that the dispatcher uses to look up
//     the right provider and that the provider uses to populate its
//     wire payload;
//   - the adapter-generated request id (RequestID), when the HTTP
//     boundary has one, for cross-layer logging and upstream request
//     correlation.
//
// Every field is typed. There is no any, no interface{}, no
// map[string]any.
type ResolvedRequest struct {
	Provider      ProviderID
	Family        string
	Model         string
	Effort        Effort
	Verbosity     string
	ContextBudget ContextBudget
	RequestID     string
	Correlation   correlation.Context

	Cursor adaptercursor.Request
	OpenAI adapteropenai.ChatRequest
}

// Valid reports whether the ResolvedRequest is well-formed enough for
// dispatch. The check is intentionally minimal. The dispatcher and
// providers do their own per-call validation.
func (r ResolvedRequest) Valid() bool {
	return r.Provider.Valid() && r.Effort.Valid() && r.Model != ""
}
