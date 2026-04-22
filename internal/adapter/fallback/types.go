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
// ResolvedModel.CLIAlias by the dispatcher. SessionID, when non-empty,
// is passed through as `--session-id <uuid>`: callers derive a stable
// UUID per Cursor conversation so back-to-back invocations land in the
// same Claude Code transcript file, which in turn stabilizes the byte
// sequence the upstream prompt cache hashes against.
type Request struct {
	Model      string
	System     string
	Messages   []Message
	Tools      []Tool
	ToolChoice string
	RequestID  string
	SessionID  string

	// Resume, when true, switches the CLI invocation from
	// `--session-id <uuid>` (fresh) to `--resume <uuid>` (load from
	// disk). Callers are responsible for writing a valid synthesized
	// transcript to the path Claude will look up before setting this.
	// When true, the positional prompt is just the final user message;
	// the history rides on disk via the transcript file.
	Resume bool

	// WorkspaceDir, when non-empty, overrides the cwd of the spawned
	// claude -p subprocess. Used for Phase 3 transcript synthesis so
	// the synthesized JSONL lands in a dedicated claude projects
	// subdir rather than the daemon's scratch dir. Empty means use the
	// client's configured ScratchDir.
	WorkspaceDir string
}

// Usage is the token accounting echoed back from the result frame.
// CacheCreationInputTokens and CacheReadInputTokens are parsed from
// the stream-json result event when Claude Code reports prompt-cache
// activity; zero means either no cache markers were set or the upstream
// omitted them.
type Usage struct {
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// Result is the non-streaming return shape: the joined assistant
// text plus the usage counters from the result frame.
type Result struct {
	Text             string
	ReasoningContent string
	Refusal          string
	ToolCalls        []ToolCall
	Usage            Usage
	Stop             string // "tool_calls" when ToolCalls is non-empty, else "stop" or "refusal"
}

// StreamResult is the streaming completion outcome after stdout closes.
type StreamResult struct {
	Usage            Usage
	Stop             string
	Text             string
	ReasoningContent string
	Refusal          string
	ToolCalls        []ToolCall
}

// StreamEvent is one token of streamed assistant output for the Stream callback.
type StreamEvent struct {
	Kind string // "text" | "reasoning"
	Text string
}

// claudeEvent mirrors the subset of `claude -p --output-format
// stream-json` events the parser needs. Tolerant of unknown fields
// so future CLI additions do not break parsing.
type claudeEvent struct {
	Type       string         `json:"type"`
	Subtype    string         `json:"subtype,omitempty"`
	Message    claudeMessage  `json:"message,omitempty"`
	Usage      claudeRawUsage `json:"usage,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
	// Error is populated by claude -p when an upstream auth or API
	// error occurs ("authentication_failed", etc.). The parser logs
	// these so callers can diagnose silent CLI failures instead of
	// only seeing "exit status 1" downstream.
	Error string `json:"error,omitempty"`
	// IsError mirrors the boolean field on `result` frames; true when
	// the run aborted before producing a normal completion.
	IsError bool `json:"is_error,omitempty"`
	// APIErrorStatus is set on `result` frames after upstream HTTP
	// failures. Surfaced via slog for diagnosis.
	APIErrorStatus int `json:"api_error_status,omitempty"`
	// Result is the human-readable error string on `result` frames
	// when IsError is true (often a duplicate of the assistant text).
	Result string `json:"result,omitempty"`
}

type claudeMessage struct {
	Content []claudeContent `json:"content"`
}

type claudeContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type claudeRawUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}
