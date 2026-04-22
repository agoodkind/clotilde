// OpenAI wire mirrors for JSON round-trips; tags match internal/adapter/openai.go.
package tooltrans

import "encoding/json"

// OpenAIRequest mirrors adapter.ChatRequest JSON tags.
type OpenAIRequest struct {
	Model            string           `json:"model"`
	Messages         []OpenAIMessage  `json:"messages"`
	Stream           bool             `json:"stream,omitempty"`
	StreamOptions    *StreamOptions   `json:"stream_options,omitempty"`
	ReasoningEffort  string           `json:"reasoning_effort,omitempty"`
	Tools            []OpenAITool     `json:"tools,omitempty"`
	ToolChoice       json.RawMessage  `json:"tool_choice,omitempty"`
	Functions        []OpenAIFunction `json:"functions,omitempty"`
	FunctionCall     json.RawMessage  `json:"function_call,omitempty"`
	N                int              `json:"n,omitempty"`
	User             string           `json:"user,omitempty"`
	Temperature      *float64         `json:"temperature,omitempty"`
	TopP             *float64         `json:"top_p,omitempty"`
	MaxTokens        *int             `json:"max_tokens,omitempty"`
	MaxComplTokens   *int             `json:"max_completion_tokens,omitempty"`
	PresencePenalty  *float64         `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64         `json:"frequency_penalty,omitempty"`
	LogitBias        json.RawMessage  `json:"logit_bias,omitempty"`
	Logprobs         *bool            `json:"logprobs,omitempty"`
	TopLogprobs      *int             `json:"top_logprobs,omitempty"`
	Stop             json.RawMessage  `json:"stop,omitempty"`
	Seed             *int             `json:"seed,omitempty"`
	ResponseFormat   json.RawMessage  `json:"response_format,omitempty"`
	Audio            json.RawMessage  `json:"audio,omitempty"`
	Modalities       json.RawMessage  `json:"modalities,omitempty"`
	ParallelTools    *bool            `json:"parallel_tool_calls,omitempty"`
	Store            *bool            `json:"store,omitempty"`
	Metadata         json.RawMessage  `json:"metadata,omitempty"`
}

// StreamOptions mirrors OpenAI stream_options.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// OpenAITool is one tools[] entry.
type OpenAITool struct {
	Type     string                   `json:"type"`
	Function OpenAIToolFunctionSchema `json:"function"`
}

// OpenAIToolFunctionSchema is the function definition inside a tool.
type OpenAIToolFunctionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// OpenAIFunction is the legacy functions array entry.
type OpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// OpenAIMessage is one chat message.
//
// Reasoning / ReasoningContent carry chain-of-thought in the two
// wire conventions for non-stream responses. Reasoning
// (`message.reasoning`) matches LM Studio / vLLM / SGLang / o3-mini
// / gpt-oss. ReasoningContent (`message.reasoning_content`) matches
// DeepSeek-R1 and older Vercel AI SDK. We emit both in parallel so
// whichever consumer reads either gets the data.
type OpenAIMessage struct {
	Role             string                    `json:"role"`
	Content          json.RawMessage           `json:"content,omitempty"`
	Name             string                    `json:"name,omitempty"`
	ToolCalls        []OpenAIToolCall          `json:"tool_calls,omitempty"`
	ToolCallID       string                    `json:"tool_call_id,omitempty"`
	Reasoning        string                    `json:"reasoning,omitempty"`
	ReasoningContent string                    `json:"reasoning_content,omitempty"`
	Refusal          string                    `json:"refusal,omitempty"`
	Annotations      []OpenAIMessageAnnotation `json:"annotations,omitempty"`
}

// OpenAIMessageAnnotation mirrors OpenAI message.annotations[] entries.
type OpenAIMessageAnnotation struct {
	Type        string             `json:"type"`
	URLCitation *OpenAIURLCitation `json:"url_citation,omitempty"`
}

// OpenAIURLCitation is the url_citation object for an annotation.
type OpenAIURLCitation struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// OpenAIToolCall is one assistant-emitted tool call.
type OpenAIToolCall struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"`
	Function OpenAIToolCallFunction `json:"function,omitempty"`
}

// OpenAIToolCallFunction carries name and JSON arguments string.
type OpenAIToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// OpenAIContentPart is one element of a content-parts array. The
// ToolUseID/Content/ID/Name/Input fields capture Anthropic-style
// "tool_result" and "tool_use" parts that some clients (Cursor) embed
// inside user/assistant messages instead of using OpenAI's standard
// role:"tool" + assistant.tool_calls wire shape.
type OpenAIContentPart struct {
	Type      string               `json:"type"`
	Text      string               `json:"text,omitempty"`
	ImageURL  *OpenAIImageURLPart  `json:"image_url,omitempty"`
	Audio     *OpenAIAudioInputRef `json:"input_audio,omitempty"`
	Refusal   string               `json:"refusal,omitempty"`
	ToolUseID string               `json:"tool_use_id,omitempty"`
	Content   json.RawMessage      `json:"content,omitempty"`
	ID        string               `json:"id,omitempty"`
	Name      string               `json:"name,omitempty"`
	Input     json.RawMessage      `json:"input,omitempty"`
}

// OpenAIImageURLPart is the inner object for image_url parts.
type OpenAIImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// OpenAIAudioInputRef is inline base64 audio metadata.
type OpenAIAudioInputRef struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

// OpenAIChatResponse is the non-streaming chat completion response.
type OpenAIChatResponse struct {
	ID                string             `json:"id"`
	Object            string             `json:"object"`
	Created           int64              `json:"created"`
	Model             string             `json:"model"`
	Choices           []OpenAIChatChoice `json:"choices"`
	Usage             *OpenAIUsage       `json:"usage,omitempty"`
	SystemFingerprint string             `json:"system_fingerprint,omitempty"`
}

// OpenAIChatChoice is one completion choice.
type OpenAIChatChoice struct {
	Index        int                   `json:"index"`
	Message      OpenAIMessage         `json:"message"`
	Logprobs     *OpenAILogprobsResult `json:"logprobs,omitempty"`
	FinishReason string                `json:"finish_reason"`
}

// OpenAILogprobsResult mirrors choices[].logprobs.
type OpenAILogprobsResult struct {
	Content []OpenAILogprobToken `json:"content,omitempty"`
}

// OpenAILogprobToken is one token entry in logprobs content.
type OpenAILogprobToken struct {
	Token       string             `json:"token"`
	Logprob     float64            `json:"logprob"`
	Bytes       []int              `json:"bytes,omitempty"`
	TopLogprobs []OpenAITopLogprob `json:"top_logprobs,omitempty"`
}

// OpenAITopLogprob is one alternative token entry.
type OpenAITopLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes,omitempty"`
}

// OpenAIStreamChunk is one SSE chunk in a streaming reply.
type OpenAIStreamChunk struct {
	ID                string               `json:"id"`
	Object            string               `json:"object"`
	Created           int64                `json:"created"`
	Model             string               `json:"model"`
	Choices           []OpenAIStreamChoice `json:"choices"`
	Usage             *OpenAIUsage         `json:"usage,omitempty"`
	SystemFingerprint string               `json:"system_fingerprint,omitempty"`
}

// OpenAIStreamChoice carries the delta for one chunk.
type OpenAIStreamChoice struct {
	Index        int                   `json:"index"`
	Delta        OpenAIStreamDelta     `json:"delta"`
	Logprobs     *OpenAILogprobsResult `json:"logprobs,omitempty"`
	FinishReason *string               `json:"finish_reason"`
}

// OpenAIStreamDelta is the incremental message body.
//
// Reasoning / ReasoningContent carry chain-of-thought deltas. Two
// field names are emitted in parallel to match the two wire
// conventions in the wild:
//
//   - Reasoning: `delta.reasoning` (LM Studio, vLLM, SGLang,
//     o3-mini, gpt-oss). Cursor's cloud-side BYOK relay reads this
//     field and maps it into the native Thinking bubble on the
//     editor side, so users see the collapsible widget instead of
//     plain text.
//   - ReasoningContent: `delta.reasoning_content` (DeepSeek-R1,
//     Vercel AI SDK openai-compatible provider, gptel). Older name.
//
// Sending both is zero-cost: clients only read one. Empty values
// omit entirely per the json tags.
type OpenAIStreamDelta struct {
	Role             string           `json:"role,omitempty"`
	Content          string           `json:"content,omitempty"`
	Reasoning        string           `json:"reasoning,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	Refusal          string           `json:"refusal,omitempty"`
}

// OpenAIUsage matches OpenAI token accounting. PromptTokensDetails
// carries the breakdown OpenAI defined for prompt caching; the adapter
// populates cached_tokens from Anthropic's cache_read_input_tokens so
// clients that display the breakdown see cache efficiency.
type OpenAIUsage struct {
	PromptTokens        int                        `json:"prompt_tokens"`
	CompletionTokens    int                        `json:"completion_tokens"`
	TotalTokens         int                        `json:"total_tokens"`
	PromptTokensDetails *OpenAIPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

// OpenAIPromptTokensDetails is the OpenAI-canonical cache accounting
// sub-object. CachedTokens counts prompt tokens served from a cache
// hit (billed at 10% of input rate on the Anthropic side).
type OpenAIPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// CachedTokens returns the cached-token count from a Usage, or 0 when
// the details sub-object is absent. Helper so slog sites do not nil-check.
func (u OpenAIUsage) CachedTokens() int {
	if u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}
