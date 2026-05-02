package adapter

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
)

func TestPrepareAnthropicProviderRequestPreservesOpenAIStreamIntent(t *testing.T) {
	t.Parallel()

	server := &Server{
		anthr: anthropic.New(nil, nil, anthropic.Config{
			UserAgent:          "claude-cli/2.1.123",
			SystemPromptPrefix: "You are Claude Code.",
			CCVersion:          "2.1.123",
			CCEntrypoint:       "sdk-cli",
		}),
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	resolved := adapterresolver.ResolvedRequest{
		Model:  "claude-sonnet-4-6",
		Effort: adapterresolver.EffortMedium,
		OpenAI: adapteropenai.ChatRequest{
			Model:  "clyde-sonnet-4.6-medium-thinking",
			Stream: true,
			Messages: []adapteropenai.ChatMessage{{
				Role:    "user",
				Content: []byte(`"Say ok."`),
			}},
		},
		ContextBudget: adapterresolver.ContextBudget{InputTokens: 200000, OutputTokens: 64000},
	}

	prepared, err := server.prepareAnthropicProviderRequest(context.Background(), resolved, "req-stream")
	if err != nil {
		t.Fatalf("prepareAnthropicProviderRequest() error = %v", err)
	}
	if !prepared.Stream {
		t.Fatalf("prepared.Stream = false, want true")
	}
	if prepared.Request.Stream {
		t.Fatalf("prepared.Request.Stream = true, want false before execution")
	}
}
