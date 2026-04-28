package anthropicbackend

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func requestBuilderConfig() BuildRequestConfig {
	return BuildRequestConfig{
		SystemPromptPrefix: "prefix",
		UserAgent:          "clyde-test/1.0.0",
		CCVersion:          "1.0.0",
		CCEntrypoint:       "test",
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func requestBuilderChatRequest() adapteropenai.ChatRequest {
	maxTokens := 4096
	return adapteropenai.ChatRequest{
		Model: "clyde-opus-4-7",
		Messages: []adapteropenai.ChatMessage{
			{Role: "user", Content: []byte(`"hello"`)},
		},
		MaxTokens: &maxTokens,
	}
}

func TestBuildRequestThinkingDisplaySummarizedWhenEnabled(t *testing.T) {
	req := requestBuilderChatRequest()
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-haiku-4-5-thinking-enabled",
		ClaudeModel:     "claude-haiku-4-5-20251001",
		MaxOutputTokens: 32000,
		Thinking:        adaptermodel.ThinkingEnabled,
	}

	out, err := BuildRequest(context.Background(), req, model, "", requestBuilderConfig(), "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if out.Thinking == nil {
		t.Fatal("Thinking is nil")
	}
	if out.Thinking.Type != "enabled" {
		t.Fatalf("Thinking.Type = %q want enabled", out.Thinking.Type)
	}
	if out.Thinking.Display != "summarized" {
		t.Fatalf("Thinking.Display = %q want summarized", out.Thinking.Display)
	}
}

func TestBuildRequestOpus47EnabledNormalizesToAdaptive(t *testing.T) {
	req := requestBuilderChatRequest()
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7-medium-thinking-enabled",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        adaptermodel.ThinkingEnabled,
	}

	out, err := BuildRequest(context.Background(), req, model, adaptermodel.EffortMedium, requestBuilderConfig(), "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if out.Thinking == nil {
		t.Fatal("Thinking is nil")
	}
	if out.Thinking.Type != "adaptive" {
		t.Fatalf("Thinking.Type = %q want adaptive", out.Thinking.Type)
	}
	if out.Thinking.Display != "summarized" {
		t.Fatalf("Thinking.Display = %q want summarized", out.Thinking.Display)
	}
}

func TestBuildRequestHaikuEnabledStaysManual(t *testing.T) {
	req := requestBuilderChatRequest()
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-haiku-4-5-thinking-enabled",
		ClaudeModel:     "claude-haiku-4-5-20251001",
		MaxOutputTokens: 16000,
		Thinking:        adaptermodel.ThinkingEnabled,
	}

	out, err := BuildRequest(context.Background(), req, model, "", requestBuilderConfig(), "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if out.Thinking == nil {
		t.Fatal("Thinking is nil")
	}
	if out.Thinking.Type != "enabled" {
		t.Fatalf("Thinking.Type = %q want enabled", out.Thinking.Type)
	}
	if out.Thinking.BudgetTokens != 15999 {
		t.Fatalf("Thinking.BudgetTokens = %d want 15999", out.Thinking.BudgetTokens)
	}
}

func TestBuildRequestAddsJSONPromptWithoutDuplicatingPrefix(t *testing.T) {
	req := requestBuilderChatRequest()
	cfg := requestBuilderConfig()
	cfg.JSONSystemPrompt = "Return JSON only."
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
	}

	out, err := BuildRequest(context.Background(), req, model, "", cfg, "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if len(out.SystemBlocks) != 3 {
		t.Fatalf("SystemBlocks len = %d want 3", len(out.SystemBlocks))
	}
	if out.SystemBlocks[1].Text != "prefix" {
		t.Fatalf("prefix block = %q want prefix", out.SystemBlocks[1].Text)
	}
	if strings.Contains(out.SystemBlocks[2].Text, "prefix") {
		t.Fatalf("caller system should not duplicate prefix: %q", out.SystemBlocks[2].Text)
	}
	if !strings.Contains(out.SystemBlocks[2].Text, "Return JSON only.") {
		t.Fatalf("caller system missing JSON prompt: %q", out.SystemBlocks[2].Text)
	}
}

func TestBuildRequestOmitsFineGrainedToolStreamingBeta(t *testing.T) {
	// CLYDE-124: claude-cli does NOT send fine-grained-tool-streaming
	// even on streaming + tools requests. The captured reference at
	// research/claude-code/snapshots/latest/reference.toml proves this.
	// Sending it diverges our wire fingerprint from claude-cli's.
	req := requestBuilderChatRequest()
	req.Stream = true
	req.Tools = []adapteropenai.Tool{{
		Type: "function",
		Function: adapteropenai.ToolFunctionSchema{
			Name:        "ReadFile",
			Description: "read a file",
			Parameters:  []byte(`{"type":"object"}`),
		},
	}}
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
	}

	out, err := BuildRequest(context.Background(), req, model, "", requestBuilderConfig(), "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	for _, beta := range out.ExtraBetas {
		if beta == FineGrainedToolStreamingBeta {
			t.Fatalf("ExtraBetas=%v unexpectedly contains %q (claude-cli does not send it)", out.ExtraBetas, FineGrainedToolStreamingBeta)
		}
	}
}
