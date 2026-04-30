package provider

import adapteropenai "goodkind.io/clyde/internal/adapter/openai"

// Result is the typed return value of Provider.Execute. It captures
// the post-execution metadata the dispatcher needs for response
// finalization and the cost/usage logging pipeline.
type Result struct {
	// Usage records the token counts as reported by the upstream
	// provider. The dispatcher reuses this for the OpenAI usage
	// envelope and the runtime cost log.
	Usage adapteropenai.Usage
	// FinalResponse is the provider-owned non-streaming completion
	// envelope when Execute fully assembled the ChatResponse itself.
	// Nil means the dispatcher should finalize the response from the
	// buffered stream chunks instead.
	FinalResponse *adapteropenai.ChatResponse
	// FinishReason is the OpenAI-normalized terminal state. Empty
	// means the provider did not signal a clean termination; the
	// dispatcher treats that as `"stop"` defensively.
	FinishReason string
	// SystemFingerprint is the OpenAI system_fingerprint value the
	// adapter advertises for this response. Per-provider; the
	// dispatcher does not synthesize one if the provider leaves it
	// empty.
	SystemFingerprint string
	// ReasoningSignaled reports whether the provider produced any
	// reasoning content during the turn (i.e. extended thinking
	// fired). Drives the adapter.chat.completed reasoning_signaled
	// telemetry attribute.
	ReasoningSignaled bool
	// ReasoningVisible reports whether the reasoning content
	// surfaced to the client (some efforts hide reasoning). Drives
	// the adapter.chat.completed reasoning_visible attribute.
	ReasoningVisible bool
	// ReasoningSummary is the surfaced reasoning text (or its
	// summary) when the provider supports reasoning. Empty when the
	// turn produced no reasoning trace.
	ReasoningSummary string
	// DerivedCacheCreationTokens is the adapter-derived count of
	// tokens that contributed to a new prompt-cache entry. Used by
	// the cost log; not part of the OpenAI wire usage block.
	DerivedCacheCreationTokens int
}
