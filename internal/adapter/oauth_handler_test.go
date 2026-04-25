package adapter

import (
	"io"
	"log/slog"
	"testing"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/config"
)

func oauthTestClientIdentity() config.AdapterClientIdentity {
	return config.AdapterClientIdentity{
		BetaHeader:              "x",
		UserAgent:               "y",
		SystemPromptPrefix:      "z",
		StainlessPackageVersion: "0",
		StainlessRuntime:        "node",
		StainlessRuntimeVersion: "v0",
		CCVersion:               "1.0.0",
		CCEntrypoint:            "ci",
	}
}

func intPtr(v int) *int { return &v }

func testOAuthServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		cfg: config.AdapterConfig{
			ClientIdentity: oauthTestClientIdentity(),
		},
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		anthr: anthropic.New(nil, nil, anthropic.Config{
			SystemPromptPrefix: "prefix",
			UserAgent:          "clyde-test/1.0.0",
			CCVersion:          "1.0.0",
			CCEntrypoint:       "test",
		}),
	}
}

func testChatRequest() ChatRequest {
	return ChatRequest{
		Model: "clyde-opus-4-7",
		Messages: []ChatMessage{
			{Role: "user", Content: mustRaw(`"hello"`)},
		},
		MaxTokens: intPtr(4096),
	}
}

func mustRaw(s string) []byte {
	return []byte(s)
}

func TestBuildAnthropicWireThinkingDisplaySummarizedWhenEnabled(t *testing.T) {
	srv := testOAuthServer(t)
	req := testChatRequest()
	model := ResolvedModel{
		Alias:           "clyde-haiku-4-5-thinking-enabled",
		ClaudeModel:     "claude-haiku-4-5-20251001",
		MaxOutputTokens: 32000,
		Thinking:        ThinkingEnabled,
	}

	out, err := srv.buildAnthropicWire(req, model, "", JSONResponseSpec{})
	if err != nil {
		t.Fatalf("buildAnthropicWire: %v", err)
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

func TestBuildAnthropicWireOpus47EnabledNormalizesToAdaptive(t *testing.T) {
	srv := testOAuthServer(t)
	req := testChatRequest()
	model := ResolvedModel{
		Alias:           "clyde-opus-4-7-medium-thinking-enabled",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        ThinkingEnabled,
	}

	out, err := srv.buildAnthropicWire(req, model, EffortMedium, JSONResponseSpec{})
	if err != nil {
		t.Fatalf("buildAnthropicWire: %v", err)
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

func TestBuildAnthropicWireHaikuEnabledStaysManual(t *testing.T) {
	srv := testOAuthServer(t)
	req := testChatRequest()
	model := ResolvedModel{
		Alias:           "clyde-haiku-4-5-thinking-enabled",
		ClaudeModel:     "claude-haiku-4-5-20251001",
		MaxOutputTokens: 16000,
		Thinking:        ThinkingEnabled,
	}

	out, err := srv.buildAnthropicWire(req, model, "", JSONResponseSpec{})
	if err != nil {
		t.Fatalf("buildAnthropicWire: %v", err)
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

func TestBuildAnthropicWireThinkingDisplaySummarizedWhenAdaptive(t *testing.T) {
	srv := testOAuthServer(t)
	req := testChatRequest()
	model := ResolvedModel{
		Alias:           "clyde-opus-4-7-thinking-adaptive",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        ThinkingAdaptive,
	}

	out, err := srv.buildAnthropicWire(req, model, "", JSONResponseSpec{})
	if err != nil {
		t.Fatalf("buildAnthropicWire: %v", err)
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

func TestBuildAnthropicWireThinkingDisabledHasNoDisplay(t *testing.T) {
	srv := testOAuthServer(t)
	req := testChatRequest()
	model := ResolvedModel{
		Alias:           "clyde-opus-4-7-thinking-disabled",
		ClaudeModel:     "claude-opus-4-7",
		MaxOutputTokens: 32000,
		Thinking:        ThinkingDisabled,
	}

	out, err := srv.buildAnthropicWire(req, model, "", JSONResponseSpec{})
	if err != nil {
		t.Fatalf("buildAnthropicWire: %v", err)
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
