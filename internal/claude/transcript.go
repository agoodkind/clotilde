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

	itranscript "goodkind.io/clyde/internal/transcript"
)

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

// TranscriptStats contains cheap transcript-level counters for the TUI details
// pane. Token counts are Claude-style rough estimates, not count_tokens API
// values, so they are safe to compute on every selected transcript.
type TranscriptStats struct {
	VisibleMessages       int
	VisibleTokensEstimate int
	LastMessageTokens     int
	CompactionCount       int
	LastPreCompactTokens  int
}

// ExtractRecentMessages reads the tail of a transcript and returns the last n
// user/assistant messages with their text content truncated to maxLen chars.
func ExtractRecentMessages(transcriptPath string, n, maxLen int) []RecentMessage {
	all := LoadAllMessages(transcriptPath, maxLen)
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

	parsed, err := itranscript.Parse(f)
	if err != nil {
		return nil
	}
	turns := itranscript.ShapeConversation(parsed, itranscript.ShapeOptions{
		ToolOnly:     itranscript.ToolOnlyCompactSummary,
		MaxTextRunes: maxLen,
	})
	out := make([]RecentMessage, 0, len(turns))
	for _, turn := range turns {
		out = append(out, RecentMessage{
			Role:      turn.Role,
			Text:      turn.Text,
			Timestamp: turn.Timestamp,
		})
	}
	return out
}

// isRealModel reports whether a model string came from an actual API call.
// Claude Code writes `<synthetic>` for assistant entries it injects locally
// (interrupt notices, hook fallbacks, context refreshes). Those should not
// pollute the model column in the session list.
func isRealModel(m string) bool {
	return m != "" && m != "<synthetic>"
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

// ExtractRawModelAndLastTime reads the transcript tail once and returns the
// last assistant model ID exactly as written in the transcript plus the
// timestamp of the last entry. Returns empty string and zero time if the
// transcript is missing or unreadable.
func ExtractRawModelAndLastTime(transcriptPath string) (string, time.Time) {
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
			if e.Type == "assistant" && isRealModel(e.Message.Model) {
				lastModel = e.Message.Model
			}
		}
	})
	if err != nil {
		return "", time.Time{}
	}
	return strings.TrimSpace(lastModel), lastTime
}
