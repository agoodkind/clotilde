package anthropicbackend

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func anthropicID() anthropic.Identity {
	return anthropic.Identity{
		DeviceID:    "dev-1",
		AccountUUID: "acct-1",
		SessionID:   "sess-1",
	}
}

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

func TestBuildRequestEmitsMetadataAndContextManagement(t *testing.T) {
	req := requestBuilderChatRequest()
	stream := true
	req.Stream = stream
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7-medium-thinking-enabled",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        adaptermodel.ThinkingEnabled,
	}
	cfg := requestBuilderConfig()
	cfg.Identity = anthropicID()

	out, err := BuildRequest(context.Background(), req, model, adaptermodel.EffortMedium, cfg, "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if out.Metadata == nil {
		t.Fatal("Metadata is nil")
	}
	if !strings.Contains(out.Metadata.UserID, `"device_id":"dev-1"`) {
		t.Errorf("Metadata.UserID missing device_id: %s", out.Metadata.UserID)
	}
	if !strings.Contains(out.Metadata.UserID, `"account_uuid":"acct-1"`) {
		t.Errorf("Metadata.UserID missing account_uuid: %s", out.Metadata.UserID)
	}
	if !strings.Contains(out.Metadata.UserID, `"session_id":"sess-1"`) {
		t.Errorf("Metadata.UserID missing session_id: %s", out.Metadata.UserID)
	}
	if out.ContextManagement == nil || len(out.ContextManagement.Edits) != 1 {
		t.Fatalf("ContextManagement missing or wrong length: %+v", out.ContextManagement)
	}
	edit := out.ContextManagement.Edits[0]
	if edit.Type != "clear_thinking_20251015" || edit.Keep != "all" {
		t.Errorf("clear_thinking edit shape wrong: %+v", edit)
	}
}

func TestBuildRequestSkipsContextManagementWhenThinkingOff(t *testing.T) {
	req := requestBuilderChatRequest()
	req.Stream = true
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-haiku-4-5",
		ClaudeModel:     "claude-haiku-4-5",
		MaxOutputTokens: 4096,
	}
	out, err := BuildRequest(context.Background(), req, model, "", requestBuilderConfig(), "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if out.ContextManagement != nil {
		t.Errorf("ContextManagement should be nil when thinking is off, got %+v", out.ContextManagement)
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

// TestBuildRequestPassesThinkingAdaptiveThrough locks in the contract
// after the registry took ownership of per-family thinking_wire_mode
// mapping. BuildRequest is now a passthrough: whatever Thinking value
// the registry put on the ResolvedModel is what reaches the wire.
// Callers that need the historical opus-4-7 enabled-to-adaptive remap
// rely on the registry to apply it at construction time, not on
// BuildRequest to patch it at request time.
func TestBuildRequestPassesThinkingAdaptiveThrough(t *testing.T) {
	req := requestBuilderChatRequest()
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7-medium-thinking",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        adaptermodel.ThinkingAdaptive,
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

// TestBuildRequestPassesThinkingEnabledThrough locks in that an
// operator who explicitly sets thinking_wire_mode = "enabled" on a
// family (including claude-opus-4-7) gets the typed enabled wire shape
// with budget_tokens. The registry honors the explicit choice and the
// request builder no longer rewrites it.
func TestBuildRequestPassesThinkingEnabledThrough(t *testing.T) {
	req := requestBuilderChatRequest()
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7-medium-thinking",
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
	if out.Thinking.Type != "enabled" {
		t.Fatalf("Thinking.Type = %q want enabled", out.Thinking.Type)
	}
	if out.Thinking.BudgetTokens != 31999 {
		t.Fatalf("Thinking.BudgetTokens = %d want 31999", out.Thinking.BudgetTokens)
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

func TestBuildRequestThinkingAdaptiveKeepsSummarizedDisplay(t *testing.T) {
	req := requestBuilderChatRequest()
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7-thinking-adaptive",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        adaptermodel.ThinkingAdaptive,
	}

	out, err := BuildRequest(context.Background(), req, model, "", requestBuilderConfig(), "req-test")
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

func TestBuildRequestThinkingDisabledHasNoDisplay(t *testing.T) {
	req := requestBuilderChatRequest()
	model := adaptermodel.ResolvedModel{
		Alias:           "clyde-opus-4-7-thinking-disabled",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        adaptermodel.ThinkingDisabled,
	}

	out, err := BuildRequest(context.Background(), req, model, "", requestBuilderConfig(), "req-test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if out.Thinking == nil {
		t.Fatal("Thinking is nil")
	}
	if out.Thinking.Type != "disabled" {
		t.Fatalf("Thinking.Type = %q want disabled", out.Thinking.Type)
	}
	if out.Thinking.Display != "" {
		t.Fatalf("Thinking.Display = %q want empty", out.Thinking.Display)
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
