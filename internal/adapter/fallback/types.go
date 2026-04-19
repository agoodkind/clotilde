// Request, result, and stream-json event shapes.
package fallback

// Message is one turn in the conversation. The driver collapses the
// alternating sequence into a single prompt blob for `claude -p`
// because the CLI takes one positional prompt argument.
type Message struct {
	Role    string // "user" | "assistant" | "system" | other
	Content string
}

// Request describes one fallback dispatch. Model is the CLI short
// name (e.g. "opus", "sonnet", "haiku") taken from
// ResolvedModel.CLIAlias by the dispatcher.
type Request struct {
	Model      string
	System     string
	Messages   []Message
	Tools      []Tool
	ToolChoice string
	RequestID  string
}

// Usage is the token accounting echoed back from the result frame.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Result is the non-streaming return shape: the joined assistant
// text plus the usage counters from the result frame.
type Result struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
	Stop      string // "tool_calls" when ToolCalls is non-empty, else "stop"
}

// StreamResult is the streaming completion outcome after stdout closes.
type StreamResult struct {
	Usage     Usage
	Stop      string
	Text      string
	ToolCalls []ToolCall
}

// claudeEvent mirrors the subset of `claude -p --output-format
// stream-json` events the parser needs. Tolerant of unknown fields
// so future CLI additions do not break parsing.
type claudeEvent struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype,omitempty"`
	Message claudeMessage  `json:"message,omitempty"`
	Usage   claudeRawUsage `json:"usage,omitempty"`
}

type claudeMessage struct {
	Content []claudeContent `json:"content"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeRawUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
