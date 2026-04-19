package adapter

import (
	"strings"
	"testing"
)

const fixtureStream = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"hello "}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}
{"type":"result","total_cost_usd":0.01,"usage":{"input_tokens":12,"output_tokens":3}}
`

func TestCollectStreamJoinsAssistantText(t *testing.T) {
	text, usage, err := CollectStream(strings.NewReader(fixtureStream))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello world" {
		t.Fatalf("text = %q, want %q", text, "hello world")
	}
	if usage.PromptTokens != 12 || usage.CompletionTokens != 3 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestTranslateStreamEmitsAssistantDeltasAndFinish(t *testing.T) {
	var chunks []StreamChunk
	sink := func(c StreamChunk) error {
		chunks = append(chunks, c)
		return nil
	}
	usage, err := TranslateStream(strings.NewReader(fixtureStream), "clyde-opus-4-7-high-1m", "chatcmpl-test", sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("want at least 3 chunks, got %d", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first chunk must carry assistant role, got %q", chunks[0].Choices[0].Delta.Role)
	}
	if chunks[1].Choices[0].Delta.Role != "" {
		t.Fatalf("subsequent chunks must omit role, got %q", chunks[1].Choices[0].Delta.Role)
	}
	last := chunks[len(chunks)-1]
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Fatalf("final chunk must have finish_reason stop, got %+v", last.Choices[0])
	}
	if usage.TotalTokens != 15 {
		t.Fatalf("usage total = %d, want 15", usage.TotalTokens)
	}
}
