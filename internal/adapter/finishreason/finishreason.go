// Package finishreason maps provider terminal states to OpenAI finish_reason.
// Unknown provider reasons map to "stop", except provider refusal/content-filter
// signals which map to "content_filter".
package finishreason

import "strings"

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

// FromCodex maps Codex Responses terminal reasons or already-normalized values
// to OpenAI finish_reason. Unknown values normalize to "stop".
func FromCodex(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "tool_calls", "tool_call", "function_call", "requires_action":
		return "tool_calls"
	case "max_tokens", "max_output_tokens", "length":
		return "length"
	case "content_filter", "refusal":
		return "content_filter"
	case "stop", "completed", "complete", "end_turn", "":
		return "stop"
	default:
		return "stop"
	}
}

// FromCodexResponse maps the final Codex Responses status plus optional
// incomplete reason to OpenAI finish_reason.
func FromCodexResponse(status, incompleteReason string) string {
	if incompleteReason != "" {
		return FromCodex(incompleteReason)
	}
	return FromCodex(status)
}
