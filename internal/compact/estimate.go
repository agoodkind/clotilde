package compact

import (
	"encoding/json"
	"math"
)

// Ports Claude Code's rough token estimator so previews can use the
// same per-block heuristic the upstream CLI applies when it does not
// want the network round trip of /v1/messages/count_tokens. The source
// is at clyde-research/claude-code-source-code-full/src/services/tokenEstimation.ts
// (functions roughTokenCountEstimation and roughTokenCountEstimationForBlock).
// Staying close to that algorithm means clyde's preview numbers line
// up with the numbers Claude itself uses for in-process estimates, and
// the user sees one mental model for token counts across both tools.

// roughBytesPerToken is the default character-to-token divisor used by
// Claude's estimator. Dense JSON (2 bytes/token) is handled separately
// in roughEstimateJSON below.
const roughBytesPerToken = 4

// roughJSONBytesPerToken applies to content already shaped as JSON so
// dense structural characters are not under-counted. Matches
// bytesPerTokenForFileType('json') in tokens.ts.
const roughJSONBytesPerToken = 2

// roughImageTokens matches the conservative constant Claude's
// estimator uses for image and document blocks, chosen to align with
// microCompact's IMAGE_MAX_TOKEN_SIZE so auto-compact does not fire
// too late. See tokenEstimation.ts:411.
const roughImageTokens = 2000

// RoughEstimateBlock returns the Claude-style rough token estimate
// for a single content block. Every branch mirrors a case in
// roughTokenCountEstimationForBlock in Claude Code.
func RoughEstimateBlock(b ContentBlock) int {
	switch b.Type {
	case "text":
		return roughEstimateText(b.Text)
	case "thinking":
		return roughEstimateText(b.Thinking)
	case "redacted_thinking":
		return roughEstimateText(b.Thinking)
	case "image", "document":
		return roughImageTokens
	case "tool_use":
		// Mirrors: roughTokenCountEstimation(block.name + jsonStringify(block.input))
		payload := b.ToolName
		if len(b.ToolInput) > 0 {
			if encoded, err := json.Marshal(b.ToolInput); err == nil {
				payload += string(encoded)
			}
		}
		return roughEstimateText(payload)
	case "tool_result":
		// Mirrors: roughTokenCountEstimationForContent(block.content)
		return RoughEstimateBlocks(b.ToolContent)
	default:
		// Claude's fallback is jsonStringify(block). We use the raw line
		// bytes clyde already keeps so the accounting tracks what was
		// actually on disk.
		if len(b.Raw) > 0 {
			return roughEstimateJSON(string(b.Raw))
		}
		return 0
	}
}

// RoughEstimateBlocks sums rough estimates across a slice of blocks.
func RoughEstimateBlocks(blocks []ContentBlock) int {
	total := 0
	for _, b := range blocks {
		total += RoughEstimateBlock(b)
	}
	return total
}

// RoughEstimateEntry returns the rough token count for one transcript
// entry, handling both the content-array and plain-string forms of
// message bodies. Non-message entries (system, meta) contribute zero
// because Claude's estimator also ignores them.
func RoughEstimateEntry(e Entry) int {
	if e.Type != "user" && e.Type != "assistant" {
		return 0
	}
	if len(e.Content) > 0 {
		return RoughEstimateBlocks(e.Content)
	}
	return roughEstimateText(e.TextOnly)
}

// RoughEstimateEntries sums the rough estimate across a slice.
func RoughEstimateEntries(entries []Entry) int {
	total := 0
	for _, e := range entries {
		total += RoughEstimateEntry(e)
	}
	return total
}

// roughEstimateText divides character length by the default
// bytes-per-token ratio and rounds to the nearest integer, matching
// Math.round(content.length / 4) in the TS implementation.
func roughEstimateText(s string) int {
	if s == "" {
		return 0
	}
	return int(math.Round(float64(len(s)) / float64(roughBytesPerToken)))
}

// roughEstimateJSON uses the denser bytes-per-token ratio for blobs
// that are already JSON-shaped, to match bytesPerTokenForFileType in
// tokens.ts. Used only for the fallback path in RoughEstimateBlock.
func roughEstimateJSON(s string) int {
	if s == "" {
		return 0
	}
	return int(math.Round(float64(len(s)) / float64(roughJSONBytesPerToken)))
}
