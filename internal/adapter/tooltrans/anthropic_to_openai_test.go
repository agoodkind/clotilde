package tooltrans

import (
	"encoding/json"
	"testing"
)

func TestStreamTextTranslator(t *testing.T) {
	t.Parallel()
	tr := NewStreamTranslator("chatcmpl-s", "alias")
	var all []OpenAIStreamChunk
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
	var all []OpenAIStreamChunk
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
	d1, err := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": partial1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d2, err := json.Marshal(map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{
			"type":         "input_json_delta",
			"partial_json": partial2,
		},
	})
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

func TestStreamThinkingForwardsReasoningContent(t *testing.T) {
	t.Parallel()
	tr := NewStreamTranslator("chatcmpl-th", "alias")
	chunks, _, _, _, err := tr.HandleEvent("content_block_delta", []byte(`{
  "type":"content_block_delta",
  "index":0,
  "delta":{"type":"thinking_delta","thinking":"x"}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Choices[0].Delta.ReasoningContent != "x" {
		t.Fatalf("reasoning_content = %q", chunks[0].Choices[0].Delta.ReasoningContent)
	}
}
