package anthropicbackend

import (
	"encoding/json"
	"strings"
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func newTranslatorForTest() *StreamTranslator {
	return NewStreamTranslator("req1", "clyde-test")
}

func TestStreamTranslatorThinkingEmitsBlockquoteContent(t *testing.T) {
	tr := newTranslatorForTest()
	var out []adapteropenai.StreamChunk
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
	}{Index: 0, ContentBlock: struct {
		Type string `json:"type"`
	}{Type: "thinking"}})
	if err != nil {
		t.Fatal(err)
	}
	emit("content_block_start", startThinking)

	deltaThinking, err := json.Marshal(struct {
		Index int           `json:"index"`
		Delta *AnthSSEDelta `json:"delta"`
	}{Index: 0, Delta: &AnthSSEDelta{Type: "thinking_delta", Thinking: "step one"}})
	if err != nil {
		t.Fatal(err)
	}
	emit("content_block_delta", deltaThinking)

	var seenOpener, seenText bool
	for _, ch := range out {
		if len(ch.Choices) == 0 {
			continue
		}
		c := ch.Choices[0].Delta.Content
		if strings.Contains(c, "<!--clyde-thinking-->") {
			seenOpener = true
		}
		if strings.Contains(c, "step one") {
			seenText = true
		}
	}
	if !seenOpener || !seenText {
		t.Fatalf("missing thinking envelope in %+v", out)
	}
}

func TestStreamTextTranslator(t *testing.T) {
	t.Parallel()
	tr := NewStreamTranslator("chatcmpl-s", "alias")
	var all []adapteropenai.StreamChunk
	feed := func(name string, payload string) {
		chunks, finished, reason, _, err := tr.HandleEvent(name, []byte(payload))
		if err != nil {
			t.Fatalf("event %s: %v", name, err)
		}
		all = append(all, chunks...)
		if name == "message_stop" {
			if !finished || reason != "stop" {
				t.Fatalf("finish finished=%v reason=%q", finished, reason)
			}
		}
	}
	feed("message_start", `{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":1,"output_tokens":0}}}`)
	feed("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"b"}}`)
	feed("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"c"}}`)
	feed("content_block_stop", `{"type":"content_block_stop","index":0}`)
	feed("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)
	feed("message_stop", `{"type":"message_stop"}`)

	if len(all) != 3 {
		t.Fatalf("chunk count %d", len(all))
	}
	if all[0].Choices[0].Delta.Role != "assistant" || all[0].Choices[0].Delta.Content != "a" {
		t.Fatalf("first chunk %+v", all[0].Choices[0].Delta)
	}
	if all[1].Choices[0].Delta.Role != "" || all[1].Choices[0].Delta.Content != "b" {
		t.Fatalf("second chunk %+v", all[1].Choices[0].Delta)
	}
	if all[2].Choices[0].Delta.Content != "c" {
		t.Fatalf("third chunk %+v", all[2].Choices[0].Delta)
	}
}

func TestStreamToolTranslator(t *testing.T) {
	t.Parallel()
	tr := NewStreamTranslator("chatcmpl-t", "alias")
	var all []adapteropenai.StreamChunk
	var finishReason string
	feed := func(name string, payload string) {
		chunks, finished, reason, _, err := tr.HandleEvent(name, []byte(payload))
		if err != nil {
			t.Fatalf("event %s: %v", name, err)
		}
		all = append(all, chunks...)
		if finished {
			finishReason = reason
		}
	}
	feed("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}`)
	feed("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"get_weather","input":{}}}`)
	partial1 := string([]byte{'"', '{', '"', 'l', 'o', 'c'})
	partial2 := string([]byte{'"', ':', '"', 'N', 'Y', '"', '}'})
	d1, err := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": partial1}})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": partial2}})
	if err != nil {
		t.Fatal(err)
	}
	feed("content_block_delta", string(d1))
	feed("content_block_delta", string(d2))
	feed("content_block_stop", `{"type":"content_block_stop","index":0}`)
	feed("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`)
	feed("message_stop", `{"type":"message_stop"}`)

	if finishReason != "tool_calls" {
		t.Fatalf("finish reason %q", finishReason)
	}
	if len(all) != 3 {
		t.Fatalf("chunks %d", len(all))
	}
	if len(all[0].Choices[0].Delta.ToolCalls) != 1 || all[0].Choices[0].Delta.ToolCalls[0].Function.Arguments != "" {
		t.Fatalf("open %+v", all[0].Choices[0].Delta.ToolCalls)
	}
	want1 := string([]byte{'"', '{', '"', 'l', 'o', 'c'})
	if all[1].Choices[0].Delta.ToolCalls[0].Function.Arguments != want1 {
		t.Fatalf("arg1 %+v want %q", all[1].Choices[0].Delta.ToolCalls, want1)
	}
	want2 := string([]byte{'"', ':', '"', 'N', 'Y', '"', '}'})
	if all[2].Choices[0].Delta.ToolCalls[0].Function.Arguments != want2 {
		t.Fatalf("arg2 %+v want %q", all[2].Choices[0].Delta.ToolCalls, want2)
	}
}
