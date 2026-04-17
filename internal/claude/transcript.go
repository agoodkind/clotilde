package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// transcriptEntry represents a single line in the Claude Code transcript JSONL.
type transcriptEntry struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

var modelFamilyRegex = regexp.MustCompile(`claude-(?:\d+-)*(\w+)-\d+`)

// forEachTailLine opens a transcript file, seeks to the last tailSize bytes,
// and calls fn for each complete JSONL line in the tail. Uses bufio.Reader with
// ReadSlice so that oversized lines are drained and skipped rather than halting
// the scan (unlike bufio.Scanner which stops permanently on ErrTooLong).
// Returns a non-nil error only for unexpected I/O failures.
func forEachTailLine(transcriptPath string, tailSize int, fn func(line []byte)) error {
	if transcriptPath == "" {
		return nil
	}

	file, err := os.Open(transcriptPath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	skipFirstLine := false
	if info.Size() > int64(tailSize) {
		if _, err := file.Seek(info.Size()-int64(tailSize), io.SeekStart); err != nil {
			return err
		}
		check := make([]byte, 1)
		if _, err := file.ReadAt(check, info.Size()-int64(tailSize)-1); err == nil {
			skipFirstLine = check[0] != '\n'
		} else {
			skipFirstLine = true
		}
	}

	reader := bufio.NewReaderSize(file, tailSize)

	if skipFirstLine {
		// Drain partial first line (may span multiple ReadSlice calls).
		var drainErr error
		for {
			_, drainErr = reader.ReadSlice('\n')
			if !errors.Is(drainErr, bufio.ErrBufferFull) {
				break
			}
		}
		if drainErr == io.EOF {
			return nil
		}
		if drainErr != nil {
			return drainErr
		}
	}

	for {
		line, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			for errors.Is(readErr, bufio.ErrBufferFull) {
				_, readErr = reader.ReadSlice('\n')
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			continue
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 {
			fn(line)
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// RecentMessage holds a single user or assistant message extracted from a transcript.
type RecentMessage struct {
	Role      string    // "user" or "assistant"
	Text      string    // truncated content
	Timestamp time.Time // zero if absent or unparseable
}

// ToolUseCount is a <tool, count> pair used for transcript analytics.
type ToolUseCount struct {
	Name  string
	Count int
}

// ExtractRecentMessages reads the tail of a transcript and returns the last n
// user/assistant messages with their text content truncated to maxLen chars.
func ExtractRecentMessages(transcriptPath string, n, maxLen int) []RecentMessage {
	type msgEntry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}

	var all []RecentMessage
	_ = forEachTailLine(transcriptPath, 256*1024, func(line []byte) {
		var e msgEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return
		}
		if e.Type != "user" && e.Type != "assistant" {
			return
		}

		text := extractTextContent(e.Message.Content)
		if text == "" {
			return
		}
		if e.Type == "user" && strings.Contains(text, "<") {
			if idx := strings.Index(text, "<system-reminder>"); idx >= 0 {
				text = strings.TrimSpace(text[:idx])
			}
			if idx := strings.Index(text, "<local-command"); idx >= 0 {
				text = strings.TrimSpace(text[:idx])
			}
		}
		if text == "" {
			return
		}
		if len(text) > maxLen {
			text = text[:maxLen] + "..."
		}
		var ts time.Time
		if e.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
				ts = parsed
			}
		}
		all = append(all, RecentMessage{Role: e.Type, Text: text, Timestamp: ts})
	})

	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// LoadAllMessages reads the full transcript and returns every non-empty
// user/assistant message in order, each truncated to maxLen runes. Intended
// for the details pane when a session is selected. Large transcripts may
// produce a few thousand entries; at ~100 bytes each, that fits comfortably
// in memory without pagination.
func LoadAllMessages(transcriptPath string, maxLen int) []RecentMessage {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)

	type msgEntry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}

	var out []RecentMessage
	for scanner.Scan() {
		line := scanner.Bytes()
		var e msgEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Type != "user" && e.Type != "assistant" {
			continue
		}
		text := extractTextContent(e.Message.Content)
		if text == "" {
			continue
		}
		if e.Type == "user" {
			if idx := strings.Index(text, "<system-reminder>"); idx >= 0 {
				text = strings.TrimSpace(text[:idx])
			}
			if idx := strings.Index(text, "<local-command"); idx >= 0 {
				text = strings.TrimSpace(text[:idx])
			}
		}
		if text == "" {
			continue
		}
		if maxLen > 0 {
			if runes := []rune(text); len(runes) > maxLen {
				text = string(runes[:maxLen]) + "..."
			}
		}
		var ts time.Time
		if e.Timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, e.Timestamp); err == nil {
				ts = parsed
			}
		}
		out = append(out, RecentMessage{Role: e.Type, Text: text, Timestamp: ts})
	}
	return out
}

// ToolUseStats scans the full transcript and returns a descending count of
// the top N tool names used by the assistant. Empty if none are present.
func ToolUseStats(transcriptPath string, topN int) []ToolUseCount {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)

	type entry struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	type block struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}

	counts := make(map[string]int)
	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Type != "assistant" {
			continue
		}
		var blocks []block
		if err := json.Unmarshal(e.Message.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name != "" {
				counts[b.Name]++
			}
		}
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]ToolUseCount, 0, len(counts))
	for n, c := range counts {
		out = append(out, ToolUseCount{Name: n, Count: c})
	}
	// Sort by count desc, then name asc
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && (out[j].Count > out[j-1].Count ||
			(out[j].Count == out[j-1].Count && out[j].Name < out[j-1].Name)); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if topN > 0 && len(out) > topN {
		out = out[:topN]
	}
	return out
}

// extractTextContent pulls text from a message content field which may be
// a string or an array of content blocks [{type:"text", text:"..."}].
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try as string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	// Try as array of content blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				return strings.TrimSpace(b.Text)
			}
		}
	}
	return ""
}

// ExtractLastModel reads the transcript and returns the last model used.
// Returns the model family name (e.g. "sonnet", "opus", "haiku") or empty string if not found.
//
// For large transcripts, only the last 128KB is read. Assistant entries that
// record message.model are typically small, so the most recent one will almost
// always be within the tail. A single assistant response larger than 128KB would
// be missed, but that is an accepted tradeoff for the performance benefit.
func ExtractLastModel(transcriptPath string) string {
	var lastModel string
	err := forEachTailLine(transcriptPath, 128*1024, func(line []byte) {
		var entry transcriptEntry
		if err := json.Unmarshal(line, &entry); err == nil {
			if entry.Type == "assistant" && entry.Message.Model != "" {
				lastModel = entry.Message.Model
			}
		}
	})
	if err != nil {
		return ""
	}
	return FormatModelFamily(lastModel)
}

// FormatModelFamily extracts the model family name from the full model ID.
// e.g. "claude-sonnet-4-5-20250929" -> "sonnet"
func FormatModelFamily(fullModel string) string {
	if fullModel == "" {
		return ""
	}

	matches := modelFamilyRegex.FindStringSubmatch(fullModel)
	if len(matches) > 1 {
		return matches[1] // Return the captured family name
	}

	// Fallback: return full model if regex doesn't match
	return fullModel
}

// ExtractModelAndLastTime reads the transcript tail once and returns both the
// last model family name and the timestamp of the last entry. More efficient
// than calling ExtractLastModel and LastTranscriptTime separately.
// Returns empty string and zero time if the transcript is missing or unreadable.
func ExtractModelAndLastTime(transcriptPath string) (string, time.Time) {
	type entry struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Message   struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	var lastModel string
	var lastTime time.Time
	err := forEachTailLine(transcriptPath, 128*1024, func(line []byte) {
		var e entry
		if err := json.Unmarshal(line, &e); err == nil {
			if !e.Timestamp.IsZero() {
				lastTime = e.Timestamp
			}
			if e.Type == "assistant" && e.Message.Model != "" {
				lastModel = e.Message.Model
			}
		}
	})
	if err != nil {
		return "", time.Time{}
	}
	return FormatModelFamily(lastModel), lastTime
}
