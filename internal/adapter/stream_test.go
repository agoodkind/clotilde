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
	usage, finishReason, err := TranslateStream(strings.NewReader(fixtureStream), "clyde-opus-4-7-high-1m", "chatcmpl-test", sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finishReason != "stop" {
		t.Fatalf("finishReason = %q want stop", finishReason)
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

const fixtureStreamWithThinking = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"step one"},{"type":"text","text":"final"}]}}
{"type":"result","total_cost_usd":0.01,"usage":{"input_tokens":12,"output_tokens":3}}
`

func TestTranslateStreamEmitsThinkingBeforeVisibleText(t *testing.T) {
	var chunks []StreamChunk
	sink := func(c StreamChunk) error {
		chunks = append(chunks, c)
		return nil
	}
	_, finishReason, err := TranslateStream(strings.NewReader(fixtureStreamWithThinking), "clyde-opus-4-7-high-1m", "chatcmpl-think", sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finishReason != "stop" {
		t.Fatalf("finishReason = %q want stop", finishReason)
	}
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks, got %d", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first chunk role = %q want assistant", chunks[0].Choices[0].Delta.Role)
	}
	if chunks[0].Choices[0].Delta.Reasoning != "step one" || chunks[0].Choices[0].Delta.ReasoningContent != "step one" {
		t.Fatalf("thinking chunk = %+v", chunks[0].Choices[0].Delta)
	}
	if chunks[1].Choices[0].Delta.Role != "" || chunks[1].Choices[0].Delta.Content != "final" {
		t.Fatalf("text chunk = %+v", chunks[1].Choices[0].Delta)
	}
	if chunks[2].Choices[0].FinishReason == nil || *chunks[2].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish chunk = %+v", chunks[2].Choices[0])
	}
}

const fixtureStreamWithCache = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}
{"type":"result","total_cost_usd":0.001,"usage":{"input_tokens":120,"output_tokens":8,"cache_creation_input_tokens":640,"cache_read_input_tokens":3200}}
`

func TestCollectStreamSurfacesCacheTokens(t *testing.T) {
	_, usage, err := CollectStream(strings.NewReader(fixtureStreamWithCache))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.PromptTokens != 120 || usage.CompletionTokens != 8 {
		t.Fatalf("core usage = %+v", usage)
	}
	if usage.PromptTokensDetails == nil || usage.PromptTokensDetails.CachedTokens != 3200 {
		t.Fatalf("cache_read_tokens not surfaced in Usage: %+v", usage.PromptTokensDetails)
	}
	if usage.CachedTokens() != 3200 {
		t.Fatalf("CachedTokens helper returned %d", usage.CachedTokens())
	}
}
