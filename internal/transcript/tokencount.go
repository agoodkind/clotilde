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
// TokenContext carries everything needed to locate, identify, recover, and classify the source text.
type TokenContext struct {
	// File pointers (for direct seek to the exact bytes)
	TranscriptPath string // absolute path to the .jsonl file
	LineNumber     int    // 0-based line in the JSONL
	ByteOffset     int64  // byte offset of this line start in the file
	LineLength     int    // byte length of this JSONL line

	// Entry identity (unique keys into the transcript)
	MessageUUID string // entry UUID (primary key in transcript)
	ParentUUID  string // parentUuid (chain predecessor)
	PromptID    string // promptId field (groups a user turn + response)
	RequestID   string // Claude API requestId (links to billing)

	// Entry classification
	Role      string // "user" or "assistant"
	EntryType string // "user", "assistant", "system", "attachment"
	Source    string // hook source: "startup", "resume", "compact", "clear"

	// Session identity (multiple keys to find the session)
	SessionName    string // clotilde session name (store.Get key)
	SessionID      string // Claude session UUID (claude --resume key)
	WorkspaceRoot  string // project directory
	ClotildeRoot   string // .claude/clotilde path
	ProjectDirHash string // encoded project dir (for ~/.claude/projects/ lookup)

	// Position in conversation
	MessageIndex    int // 0-based index among all user+assistant messages
	TurnNumber      int // conversation turn (user+response = 1 turn)
	ChainDepth      int // position in parentUuid chain from root
	CompactionEpoch int // which compaction era (0 = before first compact, 1 = after first, etc.)

	// Content block info (one entry can have multiple content blocks)
	ContentBlockIndex int    // which block within the message content array
	ContentBlockType  string // "text", "tool_use", "tool_result", "thinking"

	// Git context at time of entry
	GitBranch string // branch when this entry was written
	Slug      string // Claude Code slug (conversation identifier)
	Entrypoint string // "cli", "claude-vscode", etc.
	Version    string // Claude Code version
}

func CountTokensBestEffort(apiKey, text string, ctx TokenContext) (tokens int, exact bool) {
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

	// Full text hash for exact recovery lookup
	fullHash := hex.EncodeToString(hash[:])

	// Tail preview for context
	tail := text
	if len(tail) > 100 {
		tail = tail[len(tail)-100:]
	}

	slog.Info("token_ledger",
		// Text fingerprint (for exact match without storing full text)
		"text_sha256", fullHash,
		"text_len", textLen,
		"text_head", preview,
		"text_tail", tail,
		// File pointers
		"transcript_path", ctx.TranscriptPath,
		"line_number", ctx.LineNumber,
		"byte_offset", ctx.ByteOffset,
		"line_length", ctx.LineLength,
		// Entry identity
		"message_uuid", ctx.MessageUUID,
		"parent_uuid", ctx.ParentUUID,
		"prompt_id", ctx.PromptID,
		"request_id", ctx.RequestID,
		// Classification
		"role", ctx.Role,
		"entry_type", ctx.EntryType,
		"source", ctx.Source,
		// Session identity
		"session_name", ctx.SessionName,
		"session_id", ctx.SessionID,
		"workspace_root", ctx.WorkspaceRoot,
		"project_dir_hash", ctx.ProjectDirHash,
		// Position
		"message_index", ctx.MessageIndex,
		"turn_number", ctx.TurnNumber,
		"chain_depth", ctx.ChainDepth,
		"compaction_epoch", ctx.CompactionEpoch,
		// Content block
		"content_block_index", ctx.ContentBlockIndex,
		"content_block_type", ctx.ContentBlockType,
		// Git/environment
		"git_branch", ctx.GitBranch,
		"slug", ctx.Slug,
		"entrypoint", ctx.Entrypoint,
		"version", ctx.Version,
		// Token counts
		"tiktoken_raw", tiktokenRaw,
		"tiktoken_adjusted", tiktokenCount,
		"multiplier", tokenMultiplier,
		"api_count", apiCount,
		"api_error", apiErr,
		"has_api_key", apiKey != "",
		// Derived
		"api_to_tiktoken_ratio", fmt.Sprintf("%.4f", apiToTiktoken),
		"estimate_error_pct", fmt.Sprintf("%.1f", estimateError),
		"bytes_per_token", fmt.Sprintf("%.2f", bytesPerToken),
		"timestamp", time.Now().UTC().Format(time.RFC3339Nano),
	)

	// Return API count if available, otherwise tiktoken
	if apiCount > 0 {
		return apiCount, true
	}
	return tiktokenCount, false
}
