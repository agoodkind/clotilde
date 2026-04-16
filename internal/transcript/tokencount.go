// Package transcript provides token counting with Claude API (exact) and tiktoken (fallback).
package transcript

import (
	"context"
	"fmt"
	"log/slog"
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

// CountTokensBestEffort computes tokens using BOTH tiktoken and Claude API (when key available).
// Returns the best available count and whether it's exact (API) or estimated (tiktoken).
// Both results are logged to slog for accuracy tracking.
func CountTokensBestEffort(apiKey, text string) (tokens int, exact bool) {
	textLen := len(text)

	// Always compute tiktoken estimate
	tiktokenCount := 0
	enc, err := tiktoken.GetEncoding("cl100k_base")
	if err == nil {
		raw := len(enc.Encode(text, nil, nil))
		tiktokenCount = int(float64(raw) * tokenMultiplier)
	} else {
		tiktokenCount = textLen / 4 // last resort
	}

	// Try Claude API if key available
	apiCount := 0
	apiErr := ""
	if apiKey != "" {
		if n, err := CountTokensForText(apiKey, text); err == nil {
			apiCount = n
		} else {
			apiErr = err.Error()
		}
	}

	// Log both for accuracy tracking
	slog.Debug("token count computed",
		"text_len", textLen,
		"tiktoken", tiktokenCount,
		"api", apiCount,
		"api_error", apiErr,
		"has_api_key", apiKey != "",
	)

	// Return API count if available, otherwise tiktoken
	if apiCount > 0 {
		return apiCount, true
	}
	return tiktokenCount, false
}
