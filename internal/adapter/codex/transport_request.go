package codex

import (
	"encoding/json"
)

// HTTPTransportRequest is the wire shape every Codex transport
// accepts. The websocket transport reads it via
// ResponseCreateRequestFromHTTP before sending. The name retains the
// historical "HTTP" prefix because BuildRequest, callers in
// continuation.go, and request serialization still use that name.
type HTTPTransportRequest struct {
	Model                string            `json:"model"`
	Instructions         string            `json:"instructions"`
	Store                bool              `json:"store"`
	Stream               bool              `json:"stream"`
	Include              []string          `json:"include,omitempty"`
	PromptCache          string            `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string            `json:"prompt_cache_retention,omitempty"`
	ServiceTier          string            `json:"service_tier,omitempty"`
	Text                 json.RawMessage   `json:"text,omitempty"`
	Truncation           string            `json:"truncation,omitempty"`
	ClientMetadata       map[string]string `json:"client_metadata,omitempty"`
	Reasoning            *Reasoning        `json:"reasoning,omitempty"`
	MaxCompletion        *int              `json:"max_completion_tokens,omitempty"`
	Input                []map[string]any  `json:"input"`
	Tools                []any             `json:"tools,omitempty"`
	ToolChoice           string            `json:"tool_choice,omitempty"`
	ParallelToolCalls    bool              `json:"parallel_tool_calls,omitempty"`
}
