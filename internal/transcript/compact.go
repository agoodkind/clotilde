// Package transcript provides compaction utilities for Claude Code JSONL transcripts.
package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	tiktoken "github.com/pkoukk/tiktoken-go"
)

const tokenMultiplier = 1.15 // cl100k undercounts Claude tokens by ~15%

// CompactOptions controls what the compactor strips and where it places the boundary.
type CompactOptions struct {
	// StripToolResults replaces tool_result content with a stub.
	StripToolResults bool
	// StripBefore only strips entries before this timestamp (zero = strip all).
	StripBefore time.Time
	// StripLargeBytes strips tool results and inputs larger than this (0 = use StripToolResults for all).
	StripLargeBytes int
	// KeepLast keeps the last N messages fully intact (no stripping).
	KeepLast int
	// TargetTokens is the desired post-boundary token count. Used with token counting.
	TargetTokens int
	// DryRun shows what would be removed without writing.
	DryRun bool
	// SummaryText is the compaction summary text to insert at the boundary.
	SummaryText string
	// SessionID for the new entries.
	SessionID string
}

// CompactResult holds stats from a compaction run.
type CompactResult struct {
	OriginalEntries int
	KeptEntries     int
	StrippedBlocks  int
	BoundaryLine    int
	VisibleEntries  int
	EstimatedTokens int
}

// WalkChain walks the parentUuid chain from the last entry in a JSONL file,
// returning the chain entries in order (oldest first) and the line numbers.
func WalkChain(path string) (chainLines []int, uuidToLine map[string]int, allLines []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, err
	}

	allLines = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	uuidToLine = make(map[string]int)

	// Index all UUIDs
	for i, line := range allLines {
		if line == "" {
			continue
		}
		var entry struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.UUID != "" {
			uuidToLine[entry.UUID] = i
		}
	}

	// Find last UUID
	var lastUUID string
	for i := len(allLines) - 1; i >= 0; i-- {
		if allLines[i] == "" {
			continue
		}
		var entry struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal([]byte(allLines[i]), &entry) == nil && entry.UUID != "" {
			lastUUID = entry.UUID
			break
		}
	}
	if lastUUID == "" {
		return nil, nil, nil, fmt.Errorf("no entries with UUID found")
	}

	// Walk backwards
	visited := make(map[string]bool)
	current := lastUUID
	for current != "" && !visited[current] {
		visited[current] = true
		ln, ok := uuidToLine[current]
		if !ok {
			break
		}
		chainLines = append(chainLines, ln)

		var entry struct {
			ParentUUID string `json:"parentUuid"`
			Subtype    string `json:"subtype"`
		}
		if json.Unmarshal([]byte(allLines[ln]), &entry) != nil {
			break
		}
		if entry.Subtype == "compact_boundary" {
			break
		}
		current = entry.ParentUUID
	}

	// Reverse to oldest-first
	for i, j := 0, len(chainLines)-1; i < j; i, j = i+1, j-1 {
		chainLines[i], chainLines[j] = chainLines[j], chainLines[i]
	}

	return chainLines, uuidToLine, allLines, nil
}

// FindBoundaries returns all compact_boundary line numbers in a file.
func FindBoundaries(allLines []string) []int {
	var boundaries []int
	for i, line := range allLines {
		if strings.Contains(line, "compact_boundary") {
			var entry struct {
				Subtype string `json:"subtype"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Subtype == "compact_boundary" {
				boundaries = append(boundaries, i)
			}
		}
	}
	return boundaries
}

// RemoveBoundary removes a compact_boundary and its isCompactSummary from the file,
// reconnecting the parentUuid chain across the gap.
func RemoveBoundary(allLines []string, boundaryLine int) ([]string, error) {
	if boundaryLine < 0 || boundaryLine >= len(allLines) {
		return nil, fmt.Errorf("boundary line %d out of range", boundaryLine)
	}

	// Get the boundary's parentUuid (the entry before it in the chain)
	var boundary struct {
		ParentUUID string `json:"parentUuid"`
		UUID       string `json:"uuid"`
	}
	if err := json.Unmarshal([]byte(allLines[boundaryLine]), &boundary); err != nil {
		return nil, fmt.Errorf("parsing boundary: %w", err)
	}

	// Find the summary (next line, isCompactSummary=true)
	summaryLine := boundaryLine + 1
	var summary struct {
		UUID      string `json:"uuid"`
		IsCompact bool   `json:"isCompactSummary"`
	}
	if summaryLine < len(allLines) {
		_ = json.Unmarshal([]byte(allLines[summaryLine]), &summary)
	}

	// Find the first chain entry after the summary that points to the summary
	reconnectUUID := boundary.ParentUUID
	removedUUIDs := map[string]bool{boundary.UUID: true}
	if summary.IsCompact {
		removedUUIDs[summary.UUID] = true
	}

	// Build new lines, skipping boundary and summary, reconnecting chain
	var result []string
	for i, line := range allLines {
		if i == boundaryLine {
			continue
		}
		if i == summaryLine && summary.IsCompact {
			continue
		}

		// Check if this entry's parentUuid points to a removed entry
		var entry struct {
			ParentUUID string `json:"parentUuid"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && removedUUIDs[entry.ParentUUID] {
			// Repoint to the boundary's parent
			var full map[string]json.RawMessage
			if json.Unmarshal([]byte(line), &full) == nil {
				raw, _ := json.Marshal(reconnectUUID)
				full["parentUuid"] = raw
				newLine, _ := json.Marshal(full)
				result = append(result, string(newLine))
				continue
			}
		}
		result = append(result, line)
	}

	return result, nil
}

// InsertBoundary inserts a compact_boundary + summary at a specific position in the
// parentUuid chain. chainLines must be in oldest-first order. targetStep is the index
// within chainLines where the boundary should be placed (entries after it are visible).
func InsertBoundary(allLines []string, chainLines []int, targetStep int, summaryText string) ([]string, error) {
	if targetStep < 1 || targetStep >= len(chainLines) {
		return nil, fmt.Errorf("targetStep %d out of range (1..%d)", targetStep, len(chainLines)-1)
	}

	targetLine := chainLines[targetStep]
	prevLine := chainLines[targetStep-1]

	// Parse the entries
	var prevEntry struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal([]byte(allLines[prevLine]), &prevEntry); err != nil {
		return nil, fmt.Errorf("parsing prev entry: %w", err)
	}

	var targetEntry map[string]json.RawMessage
	if err := json.Unmarshal([]byte(allLines[targetLine]), &targetEntry); err != nil {
		return nil, fmt.Errorf("parsing target entry: %w", err)
	}

	var targetMeta struct {
		Timestamp string `json:"timestamp"`
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal([]byte(allLines[targetLine]), &targetMeta)

	boundaryUUID := uuid.New().String()
	summaryUUID := uuid.New().String()

	boundary := map[string]interface{}{
		"parentUuid":      prevEntry.UUID,
		"isSidechain":     false,
		"type":            "system",
		"subtype":         "compact_boundary",
		"content":         "Conversation compacted",
		"isMeta":          false,
		"level":           "info",
		"compactMetadata": map[string]interface{}{"trigger": "manual", "preTokens": 500000},
		"uuid":            boundaryUUID,
		"timestamp":       targetMeta.Timestamp,
		"sessionId":       targetMeta.SessionID,
	}

	summaryEntry := map[string]interface{}{
		"parentUuid":                boundaryUUID,
		"isSidechain":               false,
		"promptId":                  uuid.New().String(),
		"type":                      "user",
		"message":                   map[string]interface{}{"role": "user", "content": summaryText},
		"isVisibleInTranscriptOnly": true,
		"isCompactSummary":          true,
		"uuid":                      summaryUUID,
		"timestamp":                 targetMeta.Timestamp,
		"userType":                  "external",
		"sessionId":                 targetMeta.SessionID,
	}

	// Update target entry's parentUuid
	raw, _ := json.Marshal(summaryUUID)
	targetEntry["parentUuid"] = raw

	boundaryJSON, _ := json.Marshal(boundary)
	summaryJSON, _ := json.Marshal(summaryEntry)
	targetJSON, _ := json.Marshal(targetEntry)

	// Build new file: insert boundary+summary before target line
	var result []string
	for i, line := range allLines {
		if i == targetLine {
			result = append(result, string(boundaryJSON))
			result = append(result, string(summaryJSON))
			result = append(result, string(targetJSON))
			continue
		}
		result = append(result, line)
	}

	return result, nil
}

// StripContent applies stripping rules to entries based on CompactOptions.
// Returns the modified lines and the count of stripped blocks.
func StripContent(allLines []string, chainLines []int, opts CompactOptions) ([]string, int) {
	chainSet := make(map[int]bool)
	for _, ln := range chainLines {
		chainSet[ln] = true
	}

	// Determine which lines to keep intact (last N in chain)
	keepIntact := make(map[int]bool)
	if opts.KeepLast > 0 && len(chainLines) > opts.KeepLast {
		for _, ln := range chainLines[len(chainLines)-opts.KeepLast:] {
			keepIntact[ln] = true
		}
	}

	stripped := 0
	result := make([]string, len(allLines))
	copy(result, allLines)

	for _, ln := range chainLines {
		if keepIntact[ln] {
			continue
		}

		line := allLines[ln]
		var entry struct {
			Timestamp string `json:"timestamp"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}

		// Check time filter
		if !opts.StripBefore.IsZero() && entry.Timestamp != "" {
			ts, err := time.Parse(time.RFC3339, entry.Timestamp)
			if err == nil && !ts.Before(opts.StripBefore) {
				continue
			}
		}

		var content []json.RawMessage
		if json.Unmarshal(entry.Message.Content, &content) != nil {
			continue
		}

		modified := false
		for i, block := range content {
			var b map[string]json.RawMessage
			if json.Unmarshal(block, &b) != nil {
				continue
			}

			typeRaw, ok := b["type"]
			if !ok {
				continue
			}
			var blockType string
			json.Unmarshal(typeRaw, &blockType)

			switch blockType {
			case "tool_result":
				if !opts.StripToolResults && opts.StripLargeBytes == 0 {
					continue
				}
				contentRaw, ok := b["content"]
				if !ok {
					continue
				}
				var contentStr string
				if json.Unmarshal(contentRaw, &contentStr) == nil {
					shouldStrip := opts.StripToolResults || (opts.StripLargeBytes > 0 && len(contentStr) > opts.StripLargeBytes)
					if shouldStrip {
						b["content"], _ = json.Marshal("[result stripped during compact]")
						newBlock, _ := json.Marshal(b)
						content[i] = newBlock
						modified = true
						stripped++
					}
				}

			case "tool_use":
				if opts.StripLargeBytes == 0 && !opts.StripToolResults {
					continue
				}
				inputRaw, ok := b["input"]
				if !ok {
					continue
				}
				var input map[string]json.RawMessage
				if json.Unmarshal(inputRaw, &input) != nil {
					continue
				}
				inputModified := false
				for k, v := range input {
					var s string
					if json.Unmarshal(v, &s) == nil {
						threshold := opts.StripLargeBytes
						if threshold == 0 {
							threshold = 500
						}
						if len(s) > threshold {
							input[k], _ = json.Marshal(s[:200] + "... [stripped]")
							inputModified = true
							stripped++
						}
					}
				}
				if inputModified {
					b["input"], _ = json.Marshal(input)
					newBlock, _ := json.Marshal(b)
					content[i] = newBlock
					modified = true
				}

			case "thinking":
				if opts.StripToolResults {
					content[i] = nil
					modified = true
					stripped++
				}
			}
		}

		if modified {
			// Remove nil entries (stripped thinking blocks)
			var cleaned []json.RawMessage
			for _, c := range content {
				if c != nil {
					cleaned = append(cleaned, c)
				}
			}

			// Rebuild the line
			var full map[string]json.RawMessage
			if json.Unmarshal([]byte(line), &full) == nil {
				var msg map[string]json.RawMessage
				if json.Unmarshal(full["message"], &msg) == nil {
					msg["content"], _ = json.Marshal(cleaned)
					full["message"], _ = json.Marshal(msg)
					newLine, _ := json.Marshal(full)
					result[ln] = string(newLine)
				}
			}
		}
	}

	return result, stripped
}

// EstimateTokens estimates the token count for a set of chain entries using
// tiktoken cl100k_base encoding with a 1.15x multiplier to approximate Claude's tokenizer.
func EstimateTokens(allLines []string, chainLines []int) (int, error) {
	enc, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		return 0, fmt.Errorf("loading cl100k_base encoding: %w", err)
	}

	// Extract just the message content text (not JSON metadata)
	var totalTokens int
	for _, ln := range chainLines {
		if ln < 0 || ln >= len(allLines) {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(allLines[ln]), &entry) != nil {
			continue
		}
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}

		// Extract text from content
		var text string
		var s string
		if json.Unmarshal(entry.Message.Content, &s) == nil {
			text = s
		} else {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(entry.Message.Content, &blocks) == nil {
				var parts []string
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" {
						parts = append(parts, b.Text)
					}
				}
				text = strings.Join(parts, "\n")
			}
		}

		if text != "" {
			totalTokens += len(enc.Encode(text, nil, nil))
		}
	}

	// Apply multiplier to approximate Claude's tokenizer
	return int(float64(totalTokens) * tokenMultiplier), nil
}

// PreviewMessages returns the first N user messages with text content
// from a set of chain entries (after a given start step).
func PreviewMessages(allLines []string, chainLines []int, startStep, count int) []string {
	var messages []string
	for i := startStep; i < len(chainLines) && len(messages) < count; i++ {
		ln := chainLines[i]
		if ln < 0 || ln >= len(allLines) {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(allLines[ln]), &entry) != nil {
			continue
		}
		if entry.Type != "user" || entry.Message.Role != "user" {
			continue
		}

		// Extract text
		var text string
		var s string
		if json.Unmarshal(entry.Message.Content, &s) == nil {
			text = s
		} else {
			var blocks []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(entry.Message.Content, &blocks) == nil {
				for _, b := range blocks {
					if b.Type == "text" && b.Text != "" {
						text = b.Text
						break
					}
				}
			}
		}

		// Skip system-reminder-only entries
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "<system-reminder") || strings.HasPrefix(text, "<local-command") || strings.HasPrefix(text, "<command-name") {
			continue
		}

		if len(text) > 120 {
			text = text[:120] + "..."
		}
		messages = append(messages, text)
	}
	return messages
}

// CompactQuickStats holds lightweight stats gathered without building the full UUID chain.
type CompactQuickStats struct {
	TotalEntries     int
	Compactions      int
	LastCompactTime  time.Time
	EntriesInContext int // entries after last compact_boundary
	EstimatedTokens  int // tiktoken cl100k * 1.15 for in-context message text
}

// QuickStats reads a transcript file line-by-line, counting total entries,
// compact_boundary occurrences, and entries after the last boundary.
// It does NOT build the full UUID chain and is safe to call in hot paths like preview panes.
func QuickStats(path string) (CompactQuickStats, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CompactQuickStats{}, err
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")

	var stats CompactQuickStats
	lastBoundaryIdx := -1

	for i, line := range lines {
		if line == "" {
			continue
		}
		stats.TotalEntries++

		if strings.Contains(line, "compact_boundary") {
			var entry struct {
				Subtype   string `json:"subtype"`
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Subtype == "compact_boundary" {
				stats.Compactions++
				lastBoundaryIdx = i
				if entry.Timestamp != "" {
					if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
						stats.LastCompactTime = t
					}
				}
			}
		}
	}

	// Count entries and tokens after the last boundary
	enc, _ := tiktoken.GetEncoding("cl100k_base")
	contextLines := lines
	if lastBoundaryIdx >= 0 {
		contextLines = lines[lastBoundaryIdx+1:]
	}

	for _, line := range contextLines {
		if line == "" {
			continue
		}
		stats.EntriesInContext++

		// Token count for user/assistant message text
		if enc != nil {
			var entry struct {
				Type    string `json:"type"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(line), &entry) != nil {
				continue
			}
			if entry.Type != "user" && entry.Type != "assistant" {
				continue
			}
			var text string
			var s string
			if json.Unmarshal(entry.Message.Content, &s) == nil {
				text = s
			} else {
				var blocks []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(entry.Message.Content, &blocks) == nil {
					for _, b := range blocks {
						if b.Type == "text" && b.Text != "" {
							text += b.Text + "\n"
						}
					}
				}
			}
			if text != "" {
				stats.EstimatedTokens += len(enc.Encode(text, nil, nil))
			}
		}
	}

	// Apply multiplier for Claude's tokenizer
	stats.EstimatedTokens = int(float64(stats.EstimatedTokens) * tokenMultiplier)

	return stats, nil
}

// CompactStats returns before/after statistics for a compaction operation.
type CompactStats struct {
	BeforeChainLen int
	AfterChainLen  int
	BeforeTokens   int
	AfterTokens    int
	BoundaryStep   int
	FirstMessages  []string // first 5 user messages after boundary
}
