package anthropicbackend

import (
	"encoding/json"
	"errors"
	"testing"

	"goodkind.io/clyde/internal/adapter/tooltrans"
)

func TestTranslateRequestSimpleUserText(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{Model: "x", Messages: []tooltrans.OpenAIMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0].Role != "user" {
		t.Fatalf("unexpected messages: %+v", out.Messages)
	}
	if len(out.Messages[0].Content) != 1 || out.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected content: %+v", out.Messages[0].Content)
	}
}

func TestTranslateRequestContentPartsTextOnly(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{Model: "x", Messages: []tooltrans.OpenAIMessage{{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hi"}]`)}}}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages[0].Content) != 1 || out.Messages[0].Content[0].Text != "hi" {
		t.Fatalf("got %+v", out.Messages[0].Content)
	}
}

func TestTranslateRequestImageDataURI(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{Model: "x", Messages: []tooltrans.OpenAIMessage{{Role: "user", Content: json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBOR"}}]`)}}}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	src := out.Messages[0].Content[0].Source
	if src == nil || src.Type != "base64" || src.MediaType != "image/png" || src.Data != "iVBOR" {
		t.Fatalf("unexpected source: %+v", src)
	}
}

func TestTranslateRequestImageHTTPSURL(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{Model: "x", Messages: []tooltrans.OpenAIMessage{{Role: "user", Content: json.RawMessage(`[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`)}}}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	src := out.Messages[0].Content[0].Source
	if src == nil || src.Type != "url" || src.URL != "https://x/y.png" {
		t.Fatalf("unexpected source: %+v", src)
	}
}

func TestTranslateRequestAudioRejected(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{Model: "x", Messages: []tooltrans.OpenAIMessage{{Role: "user", Content: json.RawMessage(`[{"type":"input_audio","input_audio":{"data":"qqq"}}]`)}}}
	_, err := TranslateRequest(req, "", 64)
	if err == nil || !errors.Is(err, ErrAudioUnsupported) {
		t.Fatalf("expected ErrAudioUnsupported, got %v", err)
	}
}

func TestTranslateRequestToolsTranslated(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hi"`),
		}},
		Tools: []tooltrans.OpenAITool{
			{Type: "function", Function: tooltrans.OpenAIToolFunctionSchema{Name: "a", Parameters: json.RawMessage(`{"type":"object"}`)}},
			{Type: "function", Function: tooltrans.OpenAIToolFunctionSchema{Name: "b", Description: "d", Parameters: json.RawMessage(`{"a":1}`)}},
		},
	}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 2 {
		t.Fatalf("tools: %+v", out.Tools)
	}
	if string(out.Tools[0].InputSchema) != `{"type":"object"}` || out.Tools[1].Name != "b" {
		t.Fatalf("unexpected tools: %+v", out.Tools)
	}
}

func TestTranslateRequestToolChoiceVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want *AnthToolChoice
	}{
		{"none", `"none"`, &AnthToolChoice{Type: "none"}},
		{"auto", `"auto"`, &AnthToolChoice{Type: "auto"}},
		{"required", `"required"`, &AnthToolChoice{Type: "any"}},
		{"named", `{"type":"function","function":{"name":"X"}}`, &AnthToolChoice{Type: "tool", Name: "X"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := tooltrans.OpenAIRequest{
				Model: "x",
				Messages: []tooltrans.OpenAIMessage{{
					Role:    "user",
					Content: json.RawMessage(`"hi"`),
				}},
				ToolChoice: json.RawMessage(tc.raw),
			}
			out, err := TranslateRequest(req, "", 64)
			if err != nil {
				t.Fatal(err)
			}
			if out.ToolChoice == nil || out.ToolChoice.Type != tc.want.Type || out.ToolChoice.Name != tc.want.Name {
				t.Fatalf("got %+v want %+v", out.ToolChoice, tc.want)
			}
		})
	}
}

func TestTranslateRequestParallelToolCallsFalse(t *testing.T) {
	t.Parallel()
	f := false
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hi"`),
		}},
		ParallelTools: &f,
	}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	if out.ToolChoice == nil || out.ToolChoice.Type != "auto" || !out.ToolChoice.DisableParallelToolUse {
		t.Fatalf("got %+v", out.ToolChoice)
	}
}

func TestTranslateRequestAssistantWithToolCalls(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{{
			Role:    "assistant",
			Content: json.RawMessage(`"ok"`),
			ToolCalls: []tooltrans.OpenAIToolCall{{
				ID:   "call_1",
				Type: "function",
				Function: tooltrans.OpenAIToolCallFunction{
					Name:      "get_weather",
					Arguments: `{"loc":"NY"}`,
				},
			}},
		}},
	}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	msg := out.Messages[0]
	if msg.Role != "assistant" || len(msg.Content) != 2 {
		t.Fatalf("unexpected assistant: %+v", msg)
	}
	tu := msg.Content[1]
	if tu.Type != "tool_use" || tu.Name != "get_weather" || string(tu.Input) != `{"loc":"NY"}` {
		t.Fatalf("unexpected tool block: %+v", tu)
	}
}

func TestTranslateRequestAssistantWithMultipleToolCallsPreservesOrder(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{{
			Role:    "assistant",
			Content: json.RawMessage(`"working"`),
			ToolCalls: []tooltrans.OpenAIToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: tooltrans.OpenAIToolCallFunction{
						Name:      "get_weather",
						Arguments: `{"loc":"NY"}`,
					},
				},
				{
					ID:   "call_2",
					Type: "function",
					Function: tooltrans.OpenAIToolCallFunction{
						Name:      "write_file",
						Arguments: `{"path":"out.md"}`,
					},
				},
			},
		}},
	}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	msg := out.Messages[0]
	if len(msg.Content) != 3 {
		t.Fatalf("content len = %d want 3 (%+v)", len(msg.Content), msg.Content)
	}
	if msg.Content[0].Type != "text" || msg.Content[0].Text != "working" {
		t.Fatalf("first block = %+v", msg.Content[0])
	}
	if msg.Content[1].Type != "tool_use" || msg.Content[1].Name != "get_weather" || string(msg.Content[1].Input) != `{"loc":"NY"}` {
		t.Fatalf("second block = %+v", msg.Content[1])
	}
	if msg.Content[2].Type != "tool_use" || msg.Content[2].Name != "write_file" || string(msg.Content[2].Input) != `{"path":"out.md"}` {
		t.Fatalf("third block = %+v", msg.Content[2])
	}
}

func TestTranslateRequestToolRoleMessage(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{{
			Role:       "tool",
			ToolCallID: "toolu_1",
			Content:    json.RawMessage(`"result"`),
		}},
	}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	b := out.Messages[0].Content[0]
	if b.Type != "tool_result" || b.ToolUseID != "toolu_1" || b.ResultContent != "result" {
		t.Fatalf("unexpected block: %+v", b)
	}
}

func TestTranslateRequestRoleAlternationMerge(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{
			{Role: "user", Content: json.RawMessage(`"a"`)},
			{Role: "user", Content: json.RawMessage(`"b"`)},
		},
	}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("expected merged user, got %d", len(out.Messages))
	}
	if len(out.Messages[0].Content) != 2 {
		t.Fatalf("content blocks: %+v", out.Messages[0].Content)
	}
}

func TestTranslateRequestSystemPrefixIdempotent(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{{
			Role:    "system",
			Content: json.RawMessage(`"SYS\n\nalready"`),
		}},
	}
	out, err := TranslateRequest(req, "SYS", 64)
	if err != nil {
		t.Fatal(err)
	}
	if out.System != "SYS\n\nalready" {
		t.Fatalf("system: %q", out.System)
	}
}

func TestTranslateRequestLegacyFunctions(t *testing.T) {
	t.Parallel()
	req := tooltrans.OpenAIRequest{
		Model: "x",
		Messages: []tooltrans.OpenAIMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hi"`),
		}},
		Functions: []tooltrans.OpenAIFunction{{
			Name:        "legacy",
			Description: "d",
			Parameters:  json.RawMessage(`{"x":1}`),
		}},
	}
	out, err := TranslateRequest(req, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 1 || out.Tools[0].Name != "legacy" {
		t.Fatalf("tools: %+v", out.Tools)
	}
}
