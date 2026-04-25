// Wire types, request/response shapes, and stream event vocabulary.
package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
)

// MaxOutputTokens is the upper bound the adapter requests when the
// caller doesn't pin one. The messages API requires max_tokens; OpenAI's
// API defaults to "model max" which we approximate generously.
const MaxOutputTokens = 8192

// Config carries wire header and body-side values for the messages
// API. Populated from the user's config; callers validate before New.
type Config struct {
	MessagesURL             string
	OAuthAnthropicVersion   string
	BetaHeader              string
	UserAgent               string
	SystemPromptPrefix      string
	StainlessPackageVersion string
	StainlessRuntime        string
	StainlessRuntimeVersion string
	CCVersion               string
	CCEntrypoint            string
}

// Client wraps an http.Client and an oauth.Manager.
type Client struct {
	http  *http.Client
	oauth OAuthSource
	cfg   Config
}

// OAuthSource is the minimum surface anthropic.Client needs from
// the oauth manager. Defined as a small interface so callers can
// swap it for a fake in tests.
type OAuthSource interface {
	Token(ctx context.Context) (string, error)
}

// CacheControl marks a block / tool / system element as a prompt
// cache breakpoint. Omitted entirely when nil; when set, the server
// treats everything up to and including this element as cacheable.
// TTL is optional: empty means 5 minutes; "1h" requires the
// extended-cache-ttl-2025-04-11 beta.
// Scope widens the cache key beyond the current org:
//   - "" (default): session-scoped, per-OAuth-token.
//   - "global": Anthropic-wide shared cache, eligible on accounts
//     that GrowthBook allowlists. Requires the
//     prompt-caching-scope-2026-01-05 beta.
//   - "org": organization-wide. Same beta.
//
// Only set scope when matching the live CLI wire shape; it changes
// the cache key and can mask bugs in breakpoint placement.
type CacheControl struct {
	Type  string `json:"type"`
	TTL   string `json:"ttl,omitempty"`
	Scope string `json:"scope,omitempty"`
}

// SystemBlock is one element of the typed array form of the system
// prompt. The API accepts system as either a plain string or an
// array of typed text blocks. The array form is required when any
// element carries a cache_control marker.
type SystemBlock struct {
	Type         string        `json:"type"` // always "text" today
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// ContentBlock is one element in a message content array or a streamed
// content block start payload subset we reuse for decoding.
type ContentBlock struct {
	Type           string          `json:"type"`
	Text           string          `json:"text,omitempty"`
	ID             string          `json:"id,omitempty"`
	Name           string          `json:"name,omitempty"`
	Input          json.RawMessage `json:"input,omitempty"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	Content        string          `json:"content,omitempty"`
	CacheReference string          `json:"cache_reference,omitempty"`
	Source         *ImageSource    `json:"source,omitempty"`
	CacheControl   *CacheControl   `json:"cache_control,omitempty"`
}

// ImageSource describes image bytes or a URL for image content blocks.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// Message is one entry in the /v1/messages "messages" array.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// MarshalJSON emits a string "content" when there is exactly one text
// block; otherwise it emits a JSON array of blocks. Both shapes are
// accepted by the server; the string form preserves prompt-cache
// behavior for the common plain-text path. When the single block
// carries a CacheControl marker the array shape is forced so the
// marker survives serialization.
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Content) == 1 && m.Content[0].Type == "text" && m.Content[0].CacheControl == nil {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			Role:    m.Role,
			Content: m.Content[0].Text,
		})
	}
	return json.Marshal(struct {
		Role    string         `json:"role"`
		Content []ContentBlock `json:"content"`
	}{
		Role:    m.Role,
		Content: m.Content,
	})
}

// Tool is the wire shape for the tools array on /v1/messages.
type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *CacheControl   `json:"cache_control,omitempty"`
}

// ToolChoice selects how the model may use tools.
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// Request is the subset of the /v1/messages body we generate.
// System and SystemBlocks are mutually exclusive on the wire; the
// custom MarshalJSON emits SystemBlocks as "system":[...] when
// populated, otherwise falls back to the plain string form for
// back-compat. Callers building typed system blocks should set
// SystemBlocks and leave System empty.
type Request struct {
	Model        string        `json:"model"`
	System       string        `json:"system,omitempty"`
	SystemBlocks []SystemBlock `json:"-"`
	Messages     []Message     `json:"messages"`
	MaxTokens    int           `json:"max_tokens"`
	Stream       bool          `json:"stream"`
	OutputConfig *OutputConfig `json:"output_config,omitempty"`
	// Thinking selects extended thinking. Three shapes are accepted
	// by the API: {type:"adaptive"} (no budget), {type:"enabled",
	// budget_tokens:N}, and {type:"disabled"}. Adaptive thinking is
	// rejected on haiku-4-5 with
	// `400 adaptive thinking is not supported on this model`;
	// enabled requires `max_tokens > budget_tokens`.
	Thinking   *Thinking   `json:"thinking,omitempty"`
	Tools      []Tool      `json:"tools,omitempty"`
	ToolChoice *ToolChoice `json:"tool_choice,omitempty"`
	// OnHeaders receives the raw response headers on successful 200 responses.
	// The callback executes during do() after the /v1/messages request is
	// committed and before status-specific processing.
	OnHeaders func(http.Header) `json:"-"`
	// ExtraBetas is appended to cfg.BetaHeader when building the
	// outbound anthropic-beta header. Use for per-model / per-request
	// flags the static config does not already include. Not serialized.
	ExtraBetas []string `json:"-"`
}

// MarshalJSON emits SystemBlocks as an array under "system" when
// present so typed blocks with cache_control markers land on the
// wire. Falls back to the plain string form otherwise.
func (r Request) MarshalJSON() ([]byte, error) {
	type alias Request
	base := alias(r)
	base.SystemBlocks = nil
	if len(r.SystemBlocks) == 0 {
		return json.Marshal(base)
	}
	// SystemBlocks wins. Clear the string form on the wire so we do
	// not double-emit system.
	base.System = ""
	encoded, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	// Splice "system":<blocks> into the encoded object. This is
	// cheaper than a second struct definition mirroring every field.
	blocks, err := json.Marshal(r.SystemBlocks)
	if err != nil {
		return nil, err
	}
	insertion := []byte(`"system":` + string(blocks) + `,`)
	// Insert just after the opening brace.
	if len(encoded) < 2 || encoded[0] != '{' {
		return nil, json.Unmarshal(encoded, nil) // should never happen
	}
	out := make([]byte, 0, len(encoded)+len(insertion))
	out = append(out, '{')
	out = append(out, insertion...)
	out = append(out, encoded[1:]...)
	return out, nil
}

// OutputConfig is the wire shape that wraps effort and (later) other
// per-request output knobs. Effort must nest under this object for
// the server to accept it when the beta header allows effort.
type OutputConfig struct {
	// Effort is one of low/medium/high/max. The adapter only sets
	// this when the registry says the family supports it; haiku-4-5
	// returns `400 This model does not support the effort parameter`.
	Effort string `json:"effort,omitempty"`
}

// Thinking is the optional extended-thinking knob. BudgetTokens uses
// `omitempty` so the adaptive shape doesn't serialize a stray
// `"budget_tokens":0` (which the API rejects).
type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

// Usage mirrors the prompt/completion token counts from the response.
// CacheCreationInputTokens is the count of input tokens written into
// the prompt cache on this request; CacheReadInputTokens is the count
// served from cache. Both are zero when prompt caching is disabled.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// Sink receives a single text delta chunk during streaming. Empty
// chunks are skipped.
type Sink func(delta string) error

// StreamEvent is a decoded streaming signal from /v1/messages SSE.
type StreamEvent struct {
	Kind        string
	Text        string
	BlockIndex  int
	ToolUseID   string
	ToolUseName string
	PartialJSON string
	StopReason  string
}

// EventSink receives structured stream events.
type EventSink func(StreamEvent) error

// Result is the aggregated outcome of a non-streaming call.
type Result struct {
	Text  string
	Usage Usage
	Stop  string
}
