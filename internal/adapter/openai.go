package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"goodkind.io/clyde/internal/adapter/chatemit"
)

// ChatRequest models the OpenAI chat.completions payload. Every
// documented top level field is parsed even when the adapter does
// not act on it, so clients pass strict schema validators and so
// the adapter never silently drops a parameter the caller depends
// on.
//
// Tool calling, vision (image content parts), and logprobs are wired on
// the direct HTTP backend, passed through verbatim on shunts, and (for
// tools) prompt-injected on the claude -p fallback.
// audio content parts are rejected with 400 on every backend; vision
// is rejected on fallback. logprobs handling is config-driven via
// [adapter.logprobs] (reject vs drop per backend) and forwarded
// verbatim on shunts.
type ChatRequest struct {
	Model            string          `json:"model"`
	Messages         []ChatMessage   `json:"messages"`
	Input            json.RawMessage `json:"input,omitempty"`
	Stream           bool            `json:"stream,omitempty"`
	StreamOptions    *StreamOptions  `json:"stream_options,omitempty"`
	ReasoningEffort  string          `json:"reasoning_effort,omitempty"`
	Reasoning        *Reasoning      `json:"reasoning,omitempty"`
	Tools            []Tool          `json:"tools,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	Functions        []Function      `json:"functions,omitempty"`
	FunctionCall     json.RawMessage `json:"function_call,omitempty"`
	N                int             `json:"n,omitempty"`
	User             string          `json:"user,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	MaxComplTokens   *int            `json:"max_completion_tokens,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	LogitBias        json.RawMessage `json:"logit_bias,omitempty"`
	Logprobs         *bool           `json:"logprobs,omitempty"`
	TopLogprobs      *int            `json:"top_logprobs,omitempty"`
	Stop             json.RawMessage `json:"stop,omitempty"`
	Seed             *int            `json:"seed,omitempty"`
	ResponseFormat   json.RawMessage `json:"response_format,omitempty"`
	Audio            json.RawMessage `json:"audio,omitempty"`
	Modalities       json.RawMessage `json:"modalities,omitempty"`
	ParallelTools    *bool           `json:"parallel_tool_calls,omitempty"`
	Store            *bool           `json:"store,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Include          []string        `json:"include,omitempty"`
}

// Reasoning mirrors Responses-style reasoning controls. Cursor sends this
// shape for ChatGPT models on BYOK.
type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Tool is one entry in the OpenAI request.tools array.
// Decoder accepts OpenAI canonical and native-messages tool wire shapes
// and normalizes both into OpenAI canonical form.
type Tool struct {
	Type     string             `json:"type"`
	Function ToolFunctionSchema `json:"function"`
}

func (t *Tool) UnmarshalJSON(raw []byte) error {
	type rawTool struct {
		Type        string              `json:"type"`
		Function    *ToolFunctionSchema `json:"function"`
		Name        string              `json:"name"`
		Description string              `json:"description"`
		Parameters  json.RawMessage     `json:"parameters"`
		InputSchema json.RawMessage     `json:"input_schema"`
		Strict      *bool               `json:"strict"`
	}

	var w rawTool
	if err := json.Unmarshal(raw, &w); err != nil {
		return err
	}

	if w.Function != nil {
		if w.Type != "" && w.Type != "function" {
			return fmt.Errorf("tool has unsupported type %q", w.Type)
		}
		t.Type = "function"
		t.Function = *w.Function
		return nil
	}

	if w.Name == "" {
		return fmt.Errorf("tool missing function schema")
	}
	switch w.Type {
	case "", "function", "custom":
		// acceptable native alternate shape
	default:
		return fmt.Errorf("tool has unsupported type %q", w.Type)
	}

	parameters := w.Parameters
	if len(parameters) == 0 {
		parameters = w.InputSchema
	}

	t.Type = "function"
	t.Function = ToolFunctionSchema{
		Name:        w.Name,
		Description: w.Description,
		Parameters:  parameters,
		Strict:      w.Strict,
	}
	return nil
}

// ToolFunctionSchema is the function definition inside a Tool.
// Parameters carries the JSON schema (kept as raw to avoid having
// to model the entire JSON Schema vocabulary).
type ToolFunctionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// Function is the legacy OpenAI functions array. The adapter
// translates legacy functions into modern tools so downstream code
// only sees one shape.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Message and streaming types are aliased from chatemit to keep all OpenAI
// wire-shape definitions single-sourced.
type (
	ChatMessage         = chatemit.ChatMessage
	MessageAnnotation   = chatemit.MessageAnnotation
	URLCitation         = chatemit.URLCitation
	ToolCall            = chatemit.ToolCall
	ToolCallFunction    = chatemit.ToolCallFunction
	LogprobsResult      = chatemit.LogprobsResult
	LogprobToken        = chatemit.LogprobToken
	TopLogprob          = chatemit.TopLogprob
	Usage               = chatemit.Usage
	ChatResponse        = chatemit.ChatResponse
	ChatChoice          = chatemit.ChatChoice
	StreamChunk         = chatemit.StreamChunk
	StreamChoice        = chatemit.StreamChoice
	StreamDelta         = chatemit.StreamDelta
	PromptTokensDetails = chatemit.PromptTokensDetails
)

// ContentPart is one element of a content-parts array on a chat
// message. Type is one of "text", "image_url", "input_audio",
// "refusal". The adapter accepts strings as a single text part.
type ContentPart struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	ImageURL *ImageURLPart  `json:"image_url,omitempty"`
	Audio    *AudioInputRef `json:"input_audio,omitempty"`
	Refusal  string         `json:"refusal,omitempty"`
}

// ImageURLPart is the inner object on a content_parts image_url
// element. URL accepts both data: URIs and https URLs.
type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// AudioInputRef carries an inline base64 audio payload from a
// content-parts message. The adapter rejects audio with 400 on
// every backend today; the direct messages path does not accept
// audio input on the message content array as of this writing).
type AudioInputRef struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

// StreamOptions mirrors OpenAI's stream_options. include_usage
// triggers a terminal chunk carrying usage counts.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Usage, PromptTokensDetails, and ChatResponse / ChatChoice / Logprobs /
// StreamChunk / StreamChoice / StreamDelta / LogprobToken / TopLogprob are
// all aliased via chatemit above to keep the OpenAI wire shape
// single-sourced in tooltrans. PromptTokensDetails carries the OpenAI
// cached_tokens breakdown; Usage.CachedTokens() helper reads it safely.

// ModelsResponse is returned from GET /v1/models.
type ModelsResponse struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

// ModelEntry is one row in the models listing.
type ModelEntry struct {
	ID            string   `json:"id"`
	Object        string   `json:"object"`
	OwnedBy       string   `json:"owned_by"`
	Context       int      `json:"context_window,omitempty"`
	ContextLength int      `json:"context_length,omitempty"`
	MaxModelLen   int      `json:"max_model_len,omitempty"`
	Efforts       []string `json:"supported_efforts,omitempty"`
	Backend       string   `json:"backend,omitempty"`
	ClaudeModel   string   `json:"claude_model,omitempty"`
}

// ErrorResponse is the error envelope the adapter returns.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody follows the OpenAI error shape.
type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Param   string `json:"param,omitempty"`
}

// FlattenContent converts a ChatMessage.Content value into a single
// string. OpenAI permits either a plain string or an array of
// content parts; the adapter accepts both and drops non text parts
// with a placeholder so the ordering survives. Used by the legacy
// runner / fallback prompt path; the native translator uses
// NormalizeContent instead so it can keep image parts intact.
func FlattenContent(raw json.RawMessage) string {
	parts, kind := NormalizeContent(raw)
	if kind == ContentKindString {
		// Single text part already flat.
		if len(parts) == 0 {
			return ""
		}
		return parts[0].Text
	}
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			b.WriteString(p.Text)
		case "image_url":
			b.WriteString("[image]")
		case "input_audio":
			b.WriteString("[audio]")
		case "refusal":
			b.WriteString("[refusal: ")
			b.WriteString(p.Refusal)
			b.WriteString("]")
		default:
			b.WriteString("[")
			b.WriteString(p.Type)
			b.WriteString("]")
		}
	}
	return b.String()
}

// ContentKind discriminates the wire shape NormalizeContent saw.
// Translators use this so a string-shaped message stays a string on
// the way out (some features only accept the string form).
type ContentKind int

const (
	// ContentKindEmpty indicates the field was absent or null.
	ContentKindEmpty ContentKind = iota
	// ContentKindString indicates a plain text string.
	ContentKindString
	// ContentKindParts indicates a content-parts array.
	ContentKindParts
)

// NormalizeContent parses a ChatMessage.Content raw JSON value into
// a typed []ContentPart slice. Plain strings become a single text
// part. Returns the part list plus a kind discriminator so callers
// can tell "the user sent a string" from "the user sent [text]".
//
// Unknown part types are preserved verbatim with their Type set so
// the per-backend translator can decide whether to reject or drop
// them; NormalizeContent itself never errors.
func NormalizeContent(raw json.RawMessage) ([]ContentPart, ContentKind) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, ContentKindEmpty
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ContentPart{{Type: "text", Text: s}}, ContentKindString
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		// Empty Type defaults to "text" so callers can keep their
		// switch statements terse.
		for i := range parts {
			if parts[i].Type == "" {
				parts[i].Type = "text"
			}
		}
		return parts, ContentKindParts
	}
	// Last resort: surface the raw bytes as one text part so the
	// translator can include them rather than silently dropping the
	// entire message.
	return []ContentPart{{Type: "text", Text: string(raw)}}, ContentKindString
}
