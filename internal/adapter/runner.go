package adapter

import adapterruntime "goodkind.io/clyde/internal/adapter/runtime"

// Deps are the host hooks the adapter needs from the daemon process.
// The daemon owns the real implementations (findRealClaude and the
// scratch directory helper); the adapter accepts them as fields so
// the package stays testable without pulling the daemon in.
type Deps struct {
	// ResolveClaude returns the path to the real claude binary.
	ResolveClaude func() (string, error)
	// ScratchDir returns a clyde owned cwd for the subprocess.
	// Empty string is tolerated; the runner falls back to the
	// current working directory.
	ScratchDir func() string
	// RequestEvents receives normalized adapter request lifecycle
	// updates so the daemon can aggregate live provider stats.
	RequestEvents adapterruntime.RequestEventSink
	// AnthropicMessagesURLOverride, when non-empty, replaces the
	// configured /v1/messages URL on the Anthropic client so its
	// outbound HTTP rides through the local MITM capture proxy.
	// The daemon populates this when [mitm].enabled_default is
	// true and the provider list includes "claude". The adapter
	// otherwise sends directly to api.anthropic.com.
	AnthropicMessagesURLOverride string
}
