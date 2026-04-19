// Package finishreason maps Anthropic stop_reason values to OpenAI finish_reason.
// Unknown Anthropic reasons map to "stop" (non-stream) or "stop" (stream),
// except "refusal" which maps to "content_filter".
package finishreason

// FromAnthropicNonStream maps Anthropic stop_reason to OpenAI finish_reason for
// collect and non-streaming paths. Unknown values normalize to "stop".
func FromAnthropicNonStream(s string) string {
	switch s {
	case "end_turn", "stop_sequence", "":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	default:
		return "stop"
	}
}

// FromAnthropicStream maps Anthropic stop_reason for streaming; unknown and empty
// values become "stop".
func FromAnthropicStream(s string) string {
	switch s {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	case "end_turn", "stop_sequence", "":
		return "stop"
	default:
		return "stop"
	}
}
