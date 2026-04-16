// Package transcript provides token counting with Claude API (exact) and tiktoken (fallback).
package transcript

import (
	"context"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	tiktoken "github.com/pkoukk/tiktoken-go"
)

// CountTokensExact uses the Anthropic count_tokens API for an exact count.
// Returns (tokens, nil) on success, or (0, error) if the API call fails.
// The caller should fall back to EstimateTokens on error.
func CountTokensExact(apiKey string, messages []anthropic.MessageParam) (int, error) {
	if apiKey == "" {
		return 0, fmt.Errorf("no API key provided")
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
		Model:    "claude-sonnet-4-6",
		Messages: messages,
	})
	if err != nil {
		return 0, fmt.Errorf("count_tokens API error: %w", err)
	}

	return int(resp.InputTokens), nil
}

// CountTokensForText is a convenience wrapper that builds a single-message
// payload from plain text and counts tokens via the API.
func CountTokensForText(apiKey, text string) (int, error) {
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(text)),
	}
	return CountTokensExact(apiKey, messages)
}

// CountTokensBestEffort tries the Claude API first, falls back to tiktoken estimate.
// Returns the count and whether it's exact (true) or estimated (false).
func CountTokensBestEffort(apiKey, text string) (tokens int, exact bool) {
	if apiKey != "" {
		if n, err := CountTokensForText(apiKey, text); err == nil {
			return n, true
		}
	}
	// Fallback: tiktoken cl100k * 1.15
	enc, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		return len(text) / 4, false
	}
	raw := len(enc.Encode(text, nil, nil))
	return int(float64(raw) * tokenMultiplier), false
}
