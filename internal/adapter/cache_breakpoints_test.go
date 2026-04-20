package adapter

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

func TestApplyCacheBreakpointsStampsLastToolAndLastUserText(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "first"}}},
		{Role: "assistant", Content: []anthropic.ContentBlock{{Type: "text", Text: "reply"}}},
		{Role: "user", Content: []anthropic.ContentBlock{
			{Type: "text", Text: "prefix"},
			{Type: "text", Text: "tail"},
		}},
	}
	tools := []anthropic.Tool{
		{Name: "first_tool"},
		{Name: "second_tool"},
	}
	applyCacheBreakpoints(msgs, tools)

	if tools[0].CacheControl != nil {
		t.Fatalf("non-last tool marked")
	}
	if tools[1].CacheControl == nil || tools[1].CacheControl.Type != "ephemeral" {
		t.Fatalf("last tool not marked ephemeral: %+v", tools[1].CacheControl)
	}
	if msgs[0].Content[0].CacheControl != nil {
		t.Fatalf("first user message marked (only last should be)")
	}
	if msgs[2].Content[0].CacheControl != nil {
		t.Fatalf("non-tail text block in last user message marked")
	}
	if msgs[2].Content[1].CacheControl == nil {
		t.Fatalf("tail text of last user message not marked")
	}
}

func TestApplyCacheBreakpointsSkipsShortConversation(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "user", Content: []anthropic.ContentBlock{{Type: "text", Text: "hi"}}},
	}
	tools := []anthropic.Tool{{Name: "only_tool"}}
	applyCacheBreakpoints(msgs, tools)

	if tools[0].CacheControl == nil {
		t.Fatalf("tool marker should apply even on single-message calls")
	}
	if msgs[0].Content[0].CacheControl != nil {
		t.Fatalf("single-message conversation should skip the conversation marker")
	}
}

func TestApplyCacheBreakpointsNoToolsOrMessagesIsNoop(t *testing.T) {
	applyCacheBreakpoints(nil, nil)
	applyCacheBreakpoints([]anthropic.Message{}, []anthropic.Tool{})
}

func TestToAnthropicAPIRequestSerializesCacheControl(t *testing.T) {
	tr := tooltrans.AnthRequest{
		System: "sys",
		Messages: []tooltrans.AnthMessage{
			{Role: "user", Content: []tooltrans.AnthContentBlock{{Type: "text", Text: "a"}}},
			{Role: "assistant", Content: []tooltrans.AnthContentBlock{{Type: "text", Text: "b"}}},
			{Role: "user", Content: []tooltrans.AnthContentBlock{{Type: "text", Text: "c"}}},
		},
		Tools: []tooltrans.AnthTool{
			{Name: "t1", InputSchema: json.RawMessage(`{}`)},
		},
		MaxTokens: 64,
	}
	req := toAnthropicAPIRequest(tr, "claude-model")
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	// Tool side marker must be present.
	if !strings.Contains(body, `"name":"t1"`) {
		t.Fatalf("missing tool name: %s", body)
	}
	if strings.Count(body, `"cache_control":{"type":"ephemeral"}`) < 2 {
		t.Fatalf("expected two ephemeral cache_control markers, got body:\n%s", body)
	}
}

func TestUsageFromAnthropicMapsCacheRead(t *testing.T) {
	in := anthropic.Usage{
		InputTokens:              120,
		OutputTokens:             30,
		CacheCreationInputTokens: 800,
		CacheReadInputTokens:     4000,
	}
	u := usageFromAnthropic(in)
	if u.PromptTokens != 120 || u.CompletionTokens != 30 || u.TotalTokens != 150 {
		t.Fatalf("unexpected core usage: %+v", u)
	}
	if u.PromptTokensDetails == nil || u.PromptTokensDetails.CachedTokens != 4000 {
		t.Fatalf("cached_tokens not mapped: %+v", u.PromptTokensDetails)
	}
}

func TestUsageFromAnthropicOmitsDetailsWhenNoCache(t *testing.T) {
	u := usageFromAnthropic(anthropic.Usage{InputTokens: 10, OutputTokens: 2})
	if u.PromptTokensDetails != nil {
		t.Fatalf("PromptTokensDetails should be nil when no cache read: %+v", u.PromptTokensDetails)
	}
}
