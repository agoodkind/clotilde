package tooltrans

import (
	"encoding/json"
	"testing"
)

func TestStreamTranslatorThinkingEmitsReasoningContent(t *testing.T) {
	tr := NewStreamTranslator("req1", "clyde-test")
	var out []OpenAIStreamChunk
	emit := func(name string, payload []byte) {
		chunks, _, _, _, err := tr.HandleEvent(name, payload)
		if err != nil {
			t.Fatalf("HandleEvent: %v", err)
		}
		out = append(out, chunks...)
	}
	msgStart, err := json.Marshal(struct {
		Message struct {
			Usage struct {
				InputTokens int `json:"input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	emit("message_start", msgStart)

	startThinking, err := json.Marshal(struct {
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
	}{
		Index: 0,
		ContentBlock: struct {
			Type string `json:"type"`
		}{Type: "thinking"},
	})
	if err != nil {
		t.Fatal(err)
	}
	emit("content_block_start", startThinking)

	deltaThinking, err := json.Marshal(struct {
		Index int           `json:"index"`
		Delta *AnthSSEDelta `json:"delta"`
	}{
		Index: 0,
		Delta: &AnthSSEDelta{Type: "thinking_delta", Thinking: "step one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	emit("content_block_delta", deltaThinking)

	var sawReasoning bool
	for _, ch := range out {
		if len(ch.Choices) == 0 {
			continue
		}
		if ch.Choices[0].Delta.ReasoningContent != "" {
			sawReasoning = true
			if ch.Choices[0].Delta.ReasoningContent != "step one" {
				t.Fatalf("reasoning = %q", ch.Choices[0].Delta.ReasoningContent)
			}
		}
	}
	if !sawReasoning {
		t.Fatalf("expected reasoning_content delta, got %d chunks", len(out))
	}
}
