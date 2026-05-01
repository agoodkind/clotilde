package render

import (
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestCollectMessageAssemblesAssistantFields(t *testing.T) {
	events := []Event{
		{Kind: EventReasoningDelta, Text: "Summary", ReasoningKind: "summary"},
		{Kind: EventReasoningDelta, Text: "details", ReasoningKind: "text"},
		{Kind: EventAssistantTextDelta, Text: "final "},
		{Kind: EventAssistantTextDelta, Text: "answer"},
		{Kind: EventAssistantRefusalDelta, Text: "declined"},
		{
			Kind: EventToolCallDelta,
			ToolCalls: []adapteropenai.ToolCall{{
				Index: 0,
				ID:    "call_1",
				Type:  "function",
				Function: adapteropenai.ToolCallFunction{
					Name: "ReadFile",
				},
			}},
		},
		{
			Kind: EventToolCallDelta,
			ToolCalls: []adapteropenai.ToolCall{{
				Index: 0,
				Function: adapteropenai.ToolCallFunction{
					Arguments: `{"path":"README.md"}`,
				},
			}},
		},
	}

	got := CollectMessage(events)
	if got.Text != "final answer" {
		t.Fatalf("text=%q", got.Text)
	}
	if got.Reasoning != "Summary\n\ndetails" {
		t.Fatalf("reasoning=%q", got.Reasoning)
	}
	if got.Refusal != "declined" {
		t.Fatalf("refusal=%q", got.Refusal)
	}
	if len(got.ToolCalls) != 1 {
		t.Fatalf("tool_calls=%d", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Function.Name != "ReadFile" || got.ToolCalls[0].Function.Arguments != `{"path":"README.md"}` {
		t.Fatalf("tool_call=%+v", got.ToolCalls[0])
	}
}
