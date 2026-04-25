package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCodexRequestIncludesReasoningEffort(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := buildCodexRequest(req, model, EffortMedium)
	if out.Reasoning == nil {
		t.Fatalf("expected reasoning stanza")
	}
	if out.Reasoning.Effort != EffortMedium {
		t.Fatalf("reasoning.effort=%q want %q", out.Reasoning.Effort, EffortMedium)
	}
}

func TestBuildCodexRequestUsesNormalizedUpstreamModel(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{
		Alias:       "clyde-gpt-5.4",
		ClaudeModel: "gpt-5.4",
	}

	out := buildCodexRequest(req, model, "")
	if out.Model != "gpt-5.4" {
		t.Fatalf("model=%q want gpt-5.4", out.Model)
	}
}

func TestBuildCodexRequestUsesSparkModelSlug(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{
		Alias:       "clyde-gpt-5.3-codex-spark",
		ClaudeModel: "gpt-5.3-codex-spark",
	}

	out := buildCodexRequest(req, model, "")
	if out.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model=%q want gpt-5.3-codex-spark", out.Model)
	}
}

func TestBuildCodexRequestUsesTieredAliasUpstreamModel(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{
		Alias:       "clyde-gpt-5.4-xhigh",
		ClaudeModel: "gpt-5.4",
	}

	out := buildCodexRequest(req, model, EffortHigh)
	if out.Model != "gpt-5.4" {
		t.Fatalf("model=%q want gpt-5.4", out.Model)
	}
	if out.Reasoning == nil || out.Reasoning.Effort != EffortHigh {
		t.Fatalf("reasoning = %+v want effort %q", out.Reasoning, EffortHigh)
	}
}

func TestBuildCodexRequestFallsBackToRequestReasoningEffort(t *testing.T) {
	req := ChatRequest{
		ReasoningEffort: "high",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := buildCodexRequest(req, model, "")
	if out.Reasoning == nil || out.Reasoning.Effort != EffortHigh {
		t.Fatalf("reasoning fallback failed: %+v", out.Reasoning)
	}
}

func TestBuildCodexRequestSkipsInvalidReasoningEffort(t *testing.T) {
	req := ChatRequest{
		ReasoningEffort: "max",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := buildCodexRequest(req, model, "")
	if out.Reasoning != nil {
		t.Fatalf("expected no reasoning stanza for invalid effort, got %+v", out.Reasoning)
	}
}

func TestBuildCodexRequestUsesResponsesReasoningFields(t *testing.T) {
	req := ChatRequest{
		Include: []string{"reasoning.encrypted_content"},
		Reasoning: &Reasoning{
			Effort:  "medium",
			Summary: "auto",
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := buildCodexRequest(req, model, "")
	if out.Reasoning == nil {
		t.Fatalf("expected reasoning stanza")
	}
	if out.Reasoning.Effort != EffortMedium {
		t.Fatalf("effort=%q want %q", out.Reasoning.Effort, EffortMedium)
	}
	if out.Reasoning.Summary != "auto" {
		t.Fatalf("summary=%q want auto", out.Reasoning.Summary)
	}
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%v", out.Include)
	}
}

func TestBuildCodexRequestAddsEncryptedReasoningIncludeAutomatically(t *testing.T) {
	req := ChatRequest{
		Reasoning: &Reasoning{
			Effort: "medium",
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := buildCodexRequest(req, model, "")
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%v", out.Include)
	}
}

func TestBuildCodexRequestUsesStablePromptCacheKeyFromMetadata(t *testing.T) {
	req := ChatRequest{
		Metadata: mustRaw(`{"conversation_id":"thread-123"}`),
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "clyde-gpt-5.4"}

	out := buildCodexRequest(req, model, "")
	if out.PromptCache != "meta:thread-123" {
		t.Fatalf("prompt_cache_key=%q want %q", out.PromptCache, "meta:thread-123")
	}
}

func TestBuildCodexRequestPrefersCursorConversationPromptCacheKey(t *testing.T) {
	req := ChatRequest{
		User:     "user-1",
		Metadata: mustRaw(`{"cursorConversationId":"conv-123"}`),
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "clyde-gpt-5.4"}

	out := buildCodexRequest(req, model, "")
	if out.PromptCache != "cursor:conv-123" {
		t.Fatalf("prompt_cache_key=%q want %q", out.PromptCache, "cursor:conv-123")
	}
}

func TestBuildCodexManagedPromptPlanUsesAssistantAnchorForIncrementalPrompt(t *testing.T) {
	plan := buildCodexManagedPromptPlan([]ChatMessage{
		{Role: "system", Content: mustRaw(`"sys"`)},
		{Role: "user", Content: mustRaw(`"first user"`)},
		{Role: "assistant", Content: mustRaw(`"first answer"`)},
		{Role: "user", Content: mustRaw(`"second user"`)},
	})
	if plan.System != "sys" {
		t.Fatalf("System=%q", plan.System)
	}
	if !strings.Contains(plan.FullPrompt, "assistant: first answer") {
		t.Fatalf("FullPrompt=%q", plan.FullPrompt)
	}
	if plan.IncrementalPrompt != "user: second user" {
		t.Fatalf("IncrementalPrompt=%q", plan.IncrementalPrompt)
	}
	if plan.AssistantAnchor != "first answer" {
		t.Fatalf("AssistantAnchor=%q", plan.AssistantAnchor)
	}
}

func TestBuildCodexManagedPromptPlanStripsThinkingEnvelopeFromAssistantAnchor(t *testing.T) {
	assistant := mustRaw(`"<!--clyde-thinking-->\n> **💭 Thinking**\n> \n\n<!--/clyde-thinking-->\n\nFinal answer.\n"`)
	plan := buildCodexManagedPromptPlan([]ChatMessage{
		{Role: "user", Content: mustRaw(`"question"`)},
		{Role: "assistant", Content: assistant},
		{Role: "user", Content: mustRaw(`"follow up"`)},
	})
	if plan.AssistantAnchor != "Final answer." {
		t.Fatalf("AssistantAnchor=%q want %q", plan.AssistantAnchor, "Final answer.")
	}
}

func TestParseCodexSSERetainsReasoningSignalWithoutVisibleText(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"delta":"Answer."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":7}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	var got strings.Builder
	res, err := parseCodexSSE(stream, func(text string) error {
		got.WriteString(text)
		return nil
	}, func(text string) error {
		got.WriteString(text)
		return nil
	})
	if err != nil {
		t.Fatalf("parseCodexSSE: %v", err)
	}
	if !res.ReasoningSignaled {
		t.Fatalf("expected reasoning signal")
	}
	if res.ReasoningVisible {
		t.Fatalf("expected no visible reasoning text")
	}
	if got.String() != "Answer." {
		t.Fatalf("streamed text = %q", got.String())
	}
}

func TestCodexReasoningPlaceholderContainsThinkingEnvelope(t *testing.T) {
	got := codexReasoningPlaceholder()
	if !strings.Contains(got, "<!--clyde-thinking-->") {
		t.Fatalf("missing thinking open: %q", got)
	}
	if !strings.Contains(got, "<!--/clyde-thinking-->") {
		t.Fatalf("missing thinking close: %q", got)
	}
	if !strings.Contains(got, "Thinking") {
		t.Fatalf("missing thinking title: %q", got)
	}
}

func TestParseCodexSSESeparatesSummaryFromReasoningBody(t *testing.T) {
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
	var got strings.Builder
	_, err := parseCodexSSE(stream, func(text string) error {
		got.WriteString(text)
		return nil
	}, func(text string) error {
		got.WriteString(text)
		return nil
	})
	if err != nil {
		t.Fatalf("parseCodexSSE: %v", err)
	}
	out := got.String()
	if !strings.Contains(out, "Exploring pet-color constraints\n> \n> I am checking combinations.") {
		t.Fatalf("expected blank-line-separated reasoning sections, got %q", out)
	}
	if !strings.Contains(out, "Final answer.") {
		t.Fatalf("missing final answer: %q", out)
	}
}

func TestCodexReasoningStateSeparatesSummarySections(t *testing.T) {
	var s codexReasoningStreamState
	first := s.nextChunk(true, "summary", codexIntPtr(0), "First heading")
	second := s.nextChunk(false, "summary", codexIntPtr(1), "Second heading")
	if !strings.Contains(first, "<!--clyde-thinking-->") {
		t.Fatalf("missing opening envelope: %q", first)
	}
	if !strings.Contains(second, "\n> \n> Second heading") {
		t.Fatalf("expected summary separation, got %q", second)
	}
}

func TestCodexReasoningStateSeparatesBoldSummaryHeadingWithoutIndexChange(t *testing.T) {
	var s codexReasoningStreamState
	_ = s.nextChunk(true, "summary", nil, "First paragraph.")
	second := s.nextChunk(false, "summary", nil, "**Second heading**")
	if !strings.Contains(second, "\n> \n> **Second heading**") {
		t.Fatalf("expected bold heading separation, got %q", second)
	}
}

func codexIntPtr(v int) *int {
	return &v
}
