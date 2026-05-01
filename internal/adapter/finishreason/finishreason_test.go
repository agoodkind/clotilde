package finishreason

import "testing"

func TestFromAnthropicStreamUnknownBecomesStop(t *testing.T) {
	if got := FromAnthropicStream("custom_stop"); got != "stop" {
		t.Fatalf("stream unknown: got %q want stop", got)
	}
}

func TestFromAnthropicStreamKnown(t *testing.T) {
	if got := FromAnthropicStream("max_tokens"); got != "length" {
		t.Fatalf("got %q", got)
	}
	if got := FromAnthropicStream("tool_use"); got != "tool_calls" {
		t.Fatalf("got %q", got)
	}
	if got := FromAnthropicStream("refusal"); got != "content_filter" {
		t.Fatalf("refusal: got %q want content_filter", got)
	}
}

func TestFromCodexKnown(t *testing.T) {
	tests := map[string]string{
		"":                  "stop",
		"completed":         "stop",
		"stop":              "stop",
		"requires_action":   "tool_calls",
		"tool_calls":        "tool_calls",
		"max_output_tokens": "length",
		"max_tokens":        "length",
		"content_filter":    "content_filter",
		"refusal":           "content_filter",
		"unexpected":        "stop",
	}
	for in, want := range tests {
		if got := FromCodex(in); got != want {
			t.Fatalf("codex %q: got %q want %q", in, got, want)
		}
	}
}

func TestFromCodexResponseUsesIncompleteReason(t *testing.T) {
	if got := FromCodexResponse("incomplete", "max_output_tokens"); got != "length" {
		t.Fatalf("got %q want length", got)
	}
	if got := FromCodexResponse("completed", ""); got != "stop" {
		t.Fatalf("got %q want stop", got)
	}
}
