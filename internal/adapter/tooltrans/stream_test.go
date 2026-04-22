package tooltrans

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamTranslatorThinkingEmitsBlockquoteContent(t *testing.T) {
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

	// Expect a content chunk containing the opening sentinel, the
	// thinking header, and the thinking text inside a blockquote.
	var seenOpener, seenText bool
	for _, ch := range out {
		if len(ch.Choices) == 0 {
			continue
		}
		c := ch.Choices[0].Delta.Content
		if strings.Contains(c, "<!--clyde-thinking-->") && strings.Contains(c, "💭 Thinking") {
			seenOpener = true
		}
		if strings.Contains(c, "step one") {
			seenText = true
		}
		// The new shape should NOT populate the reasoning fields.
		if ch.Choices[0].Delta.Reasoning != "" || ch.Choices[0].Delta.ReasoningContent != "" {
			t.Fatalf("thinking delta must not set reasoning fields (Cursor BYOK ignores them); got %+v", ch.Choices[0].Delta)
		}
	}
	if !seenOpener {
		t.Fatalf("expected opening sentinel + header, got %d chunks", len(out))
	}
	if !seenText {
		t.Fatalf("expected thinking text in content, got %d chunks", len(out))
	}
}

func TestStreamTranslatorThinkingClosesWithSentinelOnBlockStop(t *testing.T) {
	tr := NewStreamTranslator("req2", "clyde-test")
	tr.currentBlockType = "thinking"
	tr.thinkingOpen = true

	stopEv, err := json.Marshal(struct {
		Index int `json:"index"`
	}{Index: 0})
	if err != nil {
		t.Fatal(err)
	}
	chunks, _, _, _, err := tr.HandleEvent("content_block_stop", stopEv)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("want 1 closing chunk, got %d", len(chunks))
	}
	got := chunks[0].Choices[0].Delta.Content
	if !strings.Contains(got, "<!--/clyde-thinking-->") {
		t.Fatalf("closing chunk missing sentinel: %q", got)
	}
}
