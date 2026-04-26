package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/adapter/tooltrans"
)

// Phase 10 relocation: these tests live next to the parser implementation
// because they assert backend-local ParseSSE behavior.

func TestParseSSERetainsReasoningSignalWithoutVisibleText(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"delta":"Answer."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":7}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	got, res, err := collectSSE(stream)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if !res.ReasoningSignaled {
		t.Fatalf("expected reasoning signal")
	}
	if res.ReasoningVisible {
		t.Fatalf("expected no visible reasoning text")
	}
	if got != "Answer." {
		t.Fatalf("streamed text = %q", got)
	}
}

func TestParseSSEMapsIncompleteResponseToLengthFinishReason(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"delta":"Partial answer."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	_, res, err := collectSSE(stream)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "length" {
		t.Fatalf("finish_reason=%q want length", res.FinishReason)
	}
}

func TestParseSSEEmitsToolCallDeltas(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"item_id":"fc_1","delta":"{\"path\":\"out.md\"}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	r := tooltrans.NewEventRenderer("req", "alias", "codex", nil)
	var got []tooltrans.OpenAIStreamChunk
	res, err := ParseSSE(stream, r, func(ch tooltrans.OpenAIStreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", res.FinishReason)
	}
	var deltas []tooltrans.OpenAIToolCall
	for _, ch := range got {
		if len(ch.Choices) == 0 {
			continue
		}
		deltas = append(deltas, ch.Choices[0].Delta.ToolCalls...)
	}
	if len(deltas) < 2 {
		t.Fatalf("tool delta len=%d want >=2", len(deltas))
	}
	if deltas[0].Function.Name != "ReadFile" {
		t.Fatalf("first tool name=%q want ReadFile", deltas[0].Function.Name)
	}
	if deltas[1].Function.Arguments != `{"path":"out.md"}` {
		t.Fatalf("second args=%q", deltas[1].Function.Arguments)
	}
}

func TestParseSSEMapsNativeLocalShellToCursorShell(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.done",
		`data: {"item":{"id":"ls_1","type":"local_shell_call","call_id":"call_shell","status":"completed","action":{"type":"exec","command":["zsh","-lc","pwd"],"working_directory":"/repo","timeout_ms":1000}}}`,
		"",
		"event: response.completed",
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n") + "\n")
	r := tooltrans.NewEventRenderer("req", "alias", "codex", nil)
	var got []tooltrans.OpenAIStreamChunk
	res, err := ParseSSE(stream, r, func(ch tooltrans.OpenAIStreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q", res.FinishReason)
	}
	calls := collectToolCallsLocal(got)
	if len(calls) != 2 {
		t.Fatalf("tool call chunks=%d want 2: %#v", len(calls), calls)
	}
	if calls[0].Function.Name != "Shell" {
		t.Fatalf("tool name=%q want Shell", calls[0].Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[1].Function.Arguments), &args); err != nil {
		t.Fatalf("args JSON: %v", err)
	}
	if args["command"] != "pwd" || args["working_directory"] != "/repo" || args["block_until_ms"].(float64) != 1000 {
		t.Fatalf("args=%v", args)
	}
}

func TestParseSSEMapsNativeApplyPatchToCursorApplyPatch(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: out.md\n+ok\n*** End Patch\n"
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":""}}`,
		"",
		"event: response.custom_tool_call_input.delta",
		`data: {"item_id":"ct_1","call_id":"call_patch","delta":"*** Begin Patch\n"}`,
		"",
		"event: response.custom_tool_call_input.delta",
		`data: {"item_id":"ct_1","call_id":"call_patch","delta":"*** Add File: out.md\n+ok\n*** End Patch\n"}`,
		"",
		"event: response.output_item.done",
		`data: {"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":""}}`,
		"",
		"event: response.completed",
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n") + "\n")
	r := tooltrans.NewEventRenderer("req", "alias", "codex", nil)
	var got []tooltrans.OpenAIStreamChunk
	res, err := ParseSSE(stream, r, func(ch tooltrans.OpenAIStreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q", res.FinishReason)
	}
	calls := collectToolCallsLocal(got)
	if len(calls) != 3 {
		t.Fatalf("tool call chunks=%d want 3: %#v", len(calls), calls)
	}
	if calls[0].Function.Name != "ApplyPatch" {
		t.Fatalf("tool name=%q want ApplyPatch", calls[0].Function.Name)
	}
	if gotPatch := calls[1].Function.Arguments + calls[2].Function.Arguments; gotPatch != patch {
		t.Fatalf("patch args=%q want %q", gotPatch, patch)
	}
}

func TestParseSSESeparatesSummaryFromReasoningBody(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.reasoning_summary_text.delta",
		`data: {"delta":"Exploring pet-color constraints"}`,
		"",
		"event: response.reasoning_text.delta",
		`data: {"delta":"I am checking combinations."}`,
		"",
		"event: response.output_text.delta",
		`data: {"delta":"Final answer."}`,
		"",
		"event: response.completed",
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":7}}}}`,
		"",
	}, "\n") + "\n")
	got, _, err := collectSSE(stream)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if !strings.Contains(got, "Exploring pet-color constraints\n> \n> I am checking combinations.") {
		t.Fatalf("expected blank-line-separated reasoning sections, got %q", got)
	}
	if !strings.Contains(got, "Final answer.") {
		t.Fatalf("missing final answer: %q", got)
	}
}

func collectSSE(stream *strings.Reader) (string, RunResult, error) {
	r := tooltrans.NewEventRenderer("req", "alias", "codex", nil)
	var got strings.Builder
	res, err := ParseSSE(stream, r, func(ch tooltrans.OpenAIStreamChunk) error {
		if len(ch.Choices) > 0 {
			got.WriteString(ch.Choices[0].Delta.Content)
		}
		return nil
	})
	return got.String(), res, err
}

func collectToolCallsLocal(chunks []tooltrans.OpenAIStreamChunk) []tooltrans.OpenAIToolCall {
	var out []tooltrans.OpenAIToolCall
	for _, ch := range chunks {
		if len(ch.Choices) == 0 {
			continue
		}
		out = append(out, ch.Choices[0].Delta.ToolCalls...)
	}
	return out
}
