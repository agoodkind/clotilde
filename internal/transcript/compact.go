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

const tokenMultiplier = 1.20 // cl100k undercounts Claude tokens by ~20% on average (validated against API)

// CompactOptions controls what the compactor strips and where it places the boundary.
type CompactOptions struct {
	// StripToolResults replaces tool_result content with a stub.
	StripToolResults bool
	// StripThinking removes assistant thinking blocks entirely.
	// Previously this was coupled to StripToolResults; it is now an
	// independent flag so callers can strip thinking without touching tools.
	StripThinking bool
	// StripImages removes image blocks (type=image) from message content.
	// Useful when Claude Code refuses to load a session due to image
	// dimension limits: "An image in the conversation exceeds the dimension
	// limit for many-image requests (2000px)."
	StripImages bool
	// StripBefore only strips entries before this timestamp (zero = strip all).
	StripBefore time.Time
	// StripLargeBytes strips tool results and inputs larger than this (0 = use StripToolResults for all).
	StripLargeBytes int
	// KeepLast keeps the last N messages fully intact (no stripping).
	KeepLast int
	// KeepLastImages preserves the most recent N image blocks even
	// when StripImages is true. The remaining (older) image blocks
	// are still stripped. Useful when the user wants to drop image
	// history but keep the latest screenshot the model just looked at.
	// 0 means strip all when StripImages is true.
	KeepLastImages int
	// KeepLastToolResults preserves the most recent N tool_result
	// bodies even when StripToolResults is true.
	KeepLastToolResults int
	// KeepLastThinking preserves the most recent N thinking blocks.
	KeepLastThinking int
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

// StripStats is a per-block-type tally of what StripContent removed or stubbed.
type StripStats struct {
	ToolResults int // number of tool_result blocks whose body was replaced with a stub
	LargeInputs int // number of tool_use input fields truncated for size
	Thinking    int // number of thinking blocks removed
	Images      int // number of image blocks removed
}

// Total is the sum of all per-type counts.
func (s StripStats) Total() int {
	return s.ToolResults + s.LargeInputs + s.Thinking + s.Images
}

// keepSet is keyed by chain line index and then by content block index.
// A (line, block) pair present in the set is exempt from stripping
// because it fell within the most recent N occurrences of the block
// type the user asked to preserve.
type keepSet map[int]map[int]bool

// buildKeepSet walks the supplied line indexes in reverse and marks
// the most recent n blocks of blockType for preservation. Pass
// chainLines to scope the keep window to the active chain or pass
// every line index in the file to scope it globally. When n is zero
// the return value is an empty set so every block stays eligible for
// stripping.
func buildKeepSet(allLines []string, lineSet []int, blockType string, n int) keepSet {
	out := keepSet{}
	if n <= 0 {
		return out
	}
	seen := 0
	for i := len(lineSet) - 1; i >= 0 && seen < n; i-- {
		ln := lineSet[i]
		if ln < 0 || ln >= len(allLines) {
			continue
		}
		line := allLines[ln]
		var entry struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(entry.Message.Content, &blocks) != nil {
			continue
		}
		for bi := len(blocks) - 1; bi >= 0 && seen < n; bi-- {
			var b map[string]json.RawMessage
			if json.Unmarshal(blocks[bi], &b) != nil {
				continue
			}
			var t string
			if json.Unmarshal(b["type"], &t) != nil || t != blockType {
				continue
			}
			if out[ln] == nil {
				out[ln] = map[int]bool{}
			}
			out[ln][bi] = true
			seen++
		}
	}
	return out
}

// keepInSet reports whether a block at (line, blockIdx) was marked for
// preservation by buildKeepSet.
func keepInSet(s keepSet, line, blockIdx int) bool {
	if s == nil {
		return false
	}
	m, ok := s[line]
	if !ok {
		return false
	}
	return m[blockIdx]
}

// StripContent applies stripping rules to entries based on CompactOptions.
// Returns the modified lines and a per-type breakdown of what was stripped.
func StripContent(allLines []string, chainLines []int, opts CompactOptions) ([]string, StripStats) {
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

	// Build keep sets for the three "keep last N" options. The walk is
	// reverse so the most recent N occurrences of each block type are
	// marked. The strip loop below skips any (lineIdx, blockIdx) pair
	// found in the appropriate set so the user always sees the latest
	// screenshots, the latest tool results, and the latest thinking
	// regardless of the global strip flags.
	keepImages := buildKeepSet(allLines, chainLines, "image", opts.KeepLastImages)
	keepToolResults := buildKeepSet(allLines, chainLines, "tool_result", opts.KeepLastToolResults)
	keepThinking := buildKeepSet(allLines, chainLines, "thinking", opts.KeepLastThinking)

	var stats StripStats
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
				if keepInSet(keepToolResults, ln, i) {
					continue
				}
				// If asked to strip images, also dig into tool_result.content
				// arrays and remove embedded image blocks (e.g. Read on a PNG).
				if opts.StripImages {
					if contentRaw, ok := b["content"]; ok {
						var inner []json.RawMessage
						if json.Unmarshal(contentRaw, &inner) == nil {
							cleanedInner := make([]json.RawMessage, 0, len(inner))
							droppedHere := 0
							for _, ib := range inner {
								var ibMap map[string]json.RawMessage
								if json.Unmarshal(ib, &ibMap) != nil {
									cleanedInner = append(cleanedInner, ib)
									continue
								}
								var innerType string
								json.Unmarshal(ibMap["type"], &innerType)
								if innerType == "image" {
									droppedHere++
									continue
								}
								cleanedInner = append(cleanedInner, ib)
							}
							if droppedHere > 0 {
								if len(cleanedInner) == 0 {
									b["content"], _ = json.Marshal("[image stripped during compact]")
								} else {
									b["content"], _ = json.Marshal(cleanedInner)
								}
								newBlock, _ := json.Marshal(b)
								content[i] = newBlock
								modified = true
								stats.Images += droppedHere
							}
						}
					}
				}
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
						stats.ToolResults++
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
							stats.LargeInputs++
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
				if opts.StripThinking && !keepInSet(keepThinking, ln, i) {
					content[i] = nil
					modified = true
					stats.Thinking++
				}

			case "image":
				if opts.StripImages && !keepInSet(keepImages, ln, i) {
					content[i] = nil
					modified = true
					stats.Images++
				}
			}
		}

		if modified {
			// Remove nil entries (stripped thinking blocks).
			// IMPORTANT: use a non-nil zero-length slice so json.Marshal emits
			// `"content":[]` instead of `"content":null`. Claude Code's transcript
			// parser NPEs on `message.content == null`.
			cleaned := make([]json.RawMessage, 0, len(content))
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

	// Image stripping must reach beyond the active chain. Claude Code
	// reads the entire transcript when resuming a session and rejects
	// the load when any image still exceeds the dimension limit, even
	// if the offending entry sits before an old compaction boundary.
	// The chain-only loop above never visited those lines, so a user
	// who ran "strip images" still hit the resume error on the next
	// launch. This pass walks every line in the file and applies the
	// same image strip with a global keep-last window.
	if opts.StripImages {
		stripImagesEverywhere(allLines, result, &stats, opts.KeepLastImages, chainSet)
	}

	return result, stats
}

// stripImagesEverywhere strips image content from every line in the
// transcript, not just the active chain. KeepLastImages, when non
// zero, preserves the most recent N image blocks across the whole
// file. Lines already rewritten by the chain pass live in result and
// are picked up by this pass through that same buffer so chain-level
// strips and global strips compose without double counting.
func stripImagesEverywhere(allLines, result []string, stats *StripStats, keepN int, chainSet map[int]bool) {
	all := make([]int, len(result))
	for i := range result {
		all[i] = i
	}
	keep := buildKeepSet(result, all, "image", keepN)

	for ln := range result {
		// Skip lines already handled by the chain pass to avoid
		// double counting. The chain loop processed these and the
		// resulting bytes already sit in result[ln].
		if chainSet[ln] {
			continue
		}
		line := result[ln]
		if line == "" {
			continue
		}
		var entry struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
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
			var blockType string
			json.Unmarshal(b["type"], &blockType)

			switch blockType {
			case "image":
				if keepInSet(keep, ln, i) {
					continue
				}
				content[i] = nil
				modified = true
				stats.Images++
			case "tool_result":
				if keepInSet(keep, ln, i) {
					continue
				}
				inner := scrubInnerImages(b, &modified)
				if inner > 0 {
					stats.Images += inner
					newBlock, _ := json.Marshal(b)
					content[i] = newBlock
				}
			}
		}
		if !modified {
			continue
		}
		cleaned := make([]json.RawMessage, 0, len(content))
		for _, c := range content {
			if c != nil {
				cleaned = append(cleaned, c)
			}
		}
		var full map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &full) != nil {
			continue
		}
		var msg map[string]json.RawMessage
		if json.Unmarshal(full["message"], &msg) != nil {
			continue
		}
		msg["content"], _ = json.Marshal(cleaned)
		full["message"], _ = json.Marshal(msg)
		newLine, _ := json.Marshal(full)
		result[ln] = string(newLine)
	}
}

// scrubInnerImages walks a tool_result content array and removes any
// embedded image blocks. Returns the number of image blocks removed.
// modified is set to true when any rewrite happens so the caller can
// decide whether to re-emit the line.
func scrubInnerImages(b map[string]json.RawMessage, modified *bool) int {
	contentRaw, ok := b["content"]
	if !ok {
		return 0
	}
	var inner []json.RawMessage
	if json.Unmarshal(contentRaw, &inner) != nil {
		return 0
	}
	cleanedInner := make([]json.RawMessage, 0, len(inner))
	dropped := 0
	for _, ib := range inner {
		var ibMap map[string]json.RawMessage
		if json.Unmarshal(ib, &ibMap) != nil {
			cleanedInner = append(cleanedInner, ib)
			continue
		}
		var t string
		json.Unmarshal(ibMap["type"], &t)
		if t == "image" {
			dropped++
			continue
		}
		cleanedInner = append(cleanedInner, ib)
	}
	if dropped == 0 {
		return 0
	}
	if len(cleanedInner) == 0 {
		b["content"], _ = json.Marshal("[image stripped during compact]")
	} else {
		b["content"], _ = json.Marshal(cleanedInner)
	}
	*modified = true
	return dropped
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

// PreviewMessage is a single user message preview with its timestamp.
type PreviewMessage struct {
	Timestamp time.Time // parsed from the entry; zero if the field was missing or unparseable
	Text      string    // trimmed, truncated to ~120 chars
}

// PreviewMessages returns the first N user messages with text content
// from a set of chain entries (after a given start step), along with their
// timestamps.
func PreviewMessages(allLines []string, chainLines []int, startStep, count int) []PreviewMessage {
	var messages []PreviewMessage
	for i := startStep; i < len(chainLines) && len(messages) < count; i++ {
		ln := chainLines[i]
		if ln < 0 || ln >= len(allLines) {
			continue
		}
		var entry struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
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

		var ts time.Time
		if entry.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
				ts = parsed
			}
		}
		messages = append(messages, PreviewMessage{Timestamp: ts, Text: text})
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
