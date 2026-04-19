// Package finishreason maps Anthropic stop_reason values to OpenAI finish_reason.
// Non-stream and stream paths differ: unknown reasons pass through on non-stream
// and normalize to "stop" on stream.
package finishreason

// FromAnthropicNonStream maps Anthropic stop_reason to OpenAI finish_reason for
// collect and non-streaming paths. Unknown values pass through unchanged.
func FromAnthropicNonStream(s string) string {
	switch s {
	case "end_turn", "stop_sequence", "":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return s
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
	case "end_turn", "stop_sequence", "":
		return "stop"
	default:
		return "stop"
	}
}
