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

// ContentBlock is one element in a message content array or a streamed
// content block start payload subset we reuse for decoding.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Source    *ImageSource    `json:"source,omitempty"`
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
// behavior for the common plain-text path.
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Content) == 1 && m.Content[0].Type == "text" {
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
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ToolChoice selects how the model may use tools.
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// Request is the subset of the /v1/messages body we generate.
type Request struct {
	Model        string        `json:"model"`
	System       string        `json:"system,omitempty"`
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
	// ExtraBetas is appended to cfg.BetaHeader when building the
	// outbound anthropic-beta header. Use for per-model / per-request
	// flags the static config does not already include. Not serialized.
	ExtraBetas []string `json:"-"`
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
}

// Usage mirrors the prompt/completion token counts from the response.
type Usage struct {
	InputTokens  int
	OutputTokens int
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
