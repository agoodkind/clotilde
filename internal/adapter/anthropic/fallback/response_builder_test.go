package fallback

import (
	"encoding/json"
	"testing"
)

func TestBuildFinalResponseMapsTextUsageAndToolCalls(t *testing.T) {
	resp := BuildFinalResponse(FinalResponseInput{
		RequestID:         "req-1",
		ModelAlias:        "clyde-opus",
		SystemFingerprint: "fp-test",
		Result: Result{
			Text:             "plain",
			ReasoningContent: "why",
			ToolCalls: []ToolCall{{
				Name:      "Read",
				Arguments: `{"path":"README.md"}`,
			}},
			Usage: Usage{
				PromptTokens:         10,
				CompletionTokens:     5,
				TotalTokens:          15,
				CacheReadInputTokens: 7,
			},
			Stop: "tool_use",
		},
		CoerceText: func(string) (string, bool) {
			return `{"ok":true}`, true
		},
	})

	if resp.FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q", resp.FinishReason)
	}
	if resp.Response.ID != "req-1" || resp.Response.Model != "clyde-opus" || resp.Response.SystemFingerprint != "fp-test" {
		t.Fatalf("response header = %+v", resp.Response)
	}
	if resp.Usage.PromptTokensDetails == nil || resp.Usage.PromptTokensDetails.CachedTokens != 7 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	msg := resp.Response.Choices[0].Message
	var text string
	if err := json.Unmarshal(msg.Content, &text); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if text != `{"ok":true}` {
		t.Fatalf("content = %q", text)
	}
	if msg.ReasoningContent != "why" {
		t.Fatalf("reasoning = %q", msg.ReasoningContent)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "Read" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].ID == "" {
		t.Fatalf("expected synthesized tool call id")
	}
}

func TestBuildFinalResponseDoesNotCoerceRefusal(t *testing.T) {
	resp := BuildFinalResponse(FinalResponseInput{
		RequestID:  "req-1",
		ModelAlias: "model",
		Result: Result{
			Text:    "plain",
			Refusal: "blocked",
			Usage:   Usage{},
			Stop:    "refusal",
		},
		CoerceText: func(string) (string, bool) {
			return "coerced", true
		},
	})
	msg := resp.Response.Choices[0].Message
	if msg.Refusal != "blocked" {
		t.Fatalf("refusal = %q", msg.Refusal)
	}
	if string(msg.Content) != "null" {
		t.Fatalf("content = %s", msg.Content)
	}
	if resp.FinishReason != "content_filter" {
		t.Fatalf("finish = %q", resp.FinishReason)
	}
}

func TestBuildStreamPlanForBufferedToolCalls(t *testing.T) {
	plan := BuildStreamPlan(StreamPlanInput{
		RequestID:   "req-1",
		ModelAlias:  "model",
		Created:     123,
		BufferedRun: true,
		Result: StreamResult{
			ReasoningContent: "thinking",
			ToolCalls: []ToolCall{
				{Name: "Read", Arguments: `{"path":"a"}`},
				{Name: "Write", Arguments: `{"path":"b"}`},
			},
			Usage: Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
			Stop:  "tool_use",
		},
	})
	if plan.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", plan.FinishReason)
	}
	if len(plan.Chunks) != 3 {
		t.Fatalf("chunks len = %d", len(plan.Chunks))
	}
	if plan.Chunks[0].Choices[0].Delta.Role != "assistant" || plan.Chunks[0].Choices[0].Delta.ReasoningContent != "thinking" {
		t.Fatalf("first chunk = %+v", plan.Chunks[0])
	}
	if got := plan.Chunks[2].Choices[0].Delta.ToolCalls[0].Index; got != 1 {
		t.Fatalf("second tool index = %d", got)
	}
	if plan.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", plan.Usage)
	}
}

func TestBuildStreamPlanForUnbufferedRunEmitsNoReplayChunks(t *testing.T) {
	plan := BuildStreamPlan(StreamPlanInput{
		BufferedRun: false,
		Result:      StreamResult{Text: "already streamed", Stop: "end_turn"},
	})
	if len(plan.Chunks) != 0 {
		t.Fatalf("chunks = %+v", plan.Chunks)
	}
	if plan.FinishReason != "stop" {
		t.Fatalf("finish = %q", plan.FinishReason)
	}
}

func TestBuildLiveStreamChunk(t *testing.T) {
	chunk, ok := BuildLiveStreamChunk("req", "model", 99, StreamEvent{Kind: "reasoning", Text: "why"}, true)
	if !ok {
		t.Fatalf("expected chunk")
	}
	if chunk.Choices[0].Delta.Role != "assistant" || chunk.Choices[0].Delta.ReasoningContent != "why" {
		t.Fatalf("chunk = %+v", chunk)
	}
	if _, ok := BuildLiveStreamChunk("req", "model", 99, StreamEvent{Kind: "ignored"}, true); ok {
		t.Fatalf("expected unknown event to be ignored")
	}
}

func TestShouldBufferTools(t *testing.T) {
	if !ShouldBufferTools(Request{Tools: []Tool{{Name: "Read"}}, ToolChoice: "auto"}) {
		t.Fatalf("expected tools with auto choice to buffer")
	}
	if ShouldBufferTools(Request{Tools: []Tool{{Name: "Read"}}, ToolChoice: " none "}) {
		t.Fatalf("expected none choice to stream")
	}
	if ShouldBufferTools(Request{ToolChoice: "auto"}) {
		t.Fatalf("expected no tools to stream")
	}
}

func TestPathLabel(t *testing.T) {
	if got := PathLabel(Request{}); got != "fallback_prompt" {
		t.Fatalf("PathLabel = %q", got)
	}
	if got := PathLabel(Request{Resume: true}); got != "fallback_resume" {
		t.Fatalf("PathLabel resume = %q", got)
	}
}
