// Package transcript provides token counting with Claude API (exact) and tiktoken (fallback).
package transcript

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
// CountTokensBestEffort computes tokens using BOTH tiktoken and Claude API (when key available).
// transcriptPath is logged for traceability (can be empty if not from a transcript).
// sessionID and messageUUID are optional identifiers for full ledger traceability.
func CountTokensBestEffort(apiKey, text string, transcriptPath string, sessionID string, messageUUID string) (tokens int, exact bool) {
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

	// Log both for accuracy tracking and future analysis
	preview := text
	if len(preview) > 100 {
		preview = preview[:100]
	}
	hash := sha256.Sum256([]byte(text))
	hashHex := hex.EncodeToString(hash[:8]) // first 8 bytes = 16 hex chars

	// Compute detailed metrics
	tiktokenRaw := 0
	if enc != nil {
		tiktokenRaw = len(enc.Encode(text, nil, nil))
	}
	bytesPerToken := float64(0)
	if tiktokenRaw > 0 {
		bytesPerToken = float64(textLen) / float64(tiktokenRaw)
	}
	apiToTiktoken := float64(0)
	if apiCount > 0 && tiktokenRaw > 0 {
		apiToTiktoken = float64(apiCount) / float64(tiktokenRaw)
	}
	estimateError := float64(0)
	if apiCount > 0 {
		estimateError = float64(tiktokenCount-apiCount) / float64(apiCount) * 100
	}

	// Count content characteristics for analysis
	newlines := 0
	spaces := 0
	for _, c := range text {
		if c == '\n' {
			newlines++
		} else if c == ' ' || c == '\t' {
			spaces++
		}
	}

	slog.Info("token_ledger",
		"text_len", textLen,
		"text_hash", hashHex,
		"text_preview", preview,
		"transcript_path", transcriptPath,
		"session_id", sessionID,
		"message_uuid", messageUUID,
		"tiktoken_raw", tiktokenRaw,
		"tiktoken_adjusted", tiktokenCount,
		"multiplier", tokenMultiplier,
		"api_count", apiCount,
		"api_error", apiErr,
		"has_api_key", apiKey != "",
		"api_to_tiktoken_ratio", fmt.Sprintf("%.4f", apiToTiktoken),
		"estimate_error_pct", fmt.Sprintf("%.1f", estimateError),
		"bytes_per_token", fmt.Sprintf("%.2f", bytesPerToken),
		"newline_count", newlines,
		"whitespace_count", spaces,
		"whitespace_pct", fmt.Sprintf("%.1f", float64(spaces+newlines)/float64(max(textLen, 1))*100),
		"timestamp", time.Now().UTC().Format(time.RFC3339),
	)

	// Return API count if available, otherwise tiktoken
	if apiCount > 0 {
		return apiCount, true
	}
	return tiktokenCount, false
}
