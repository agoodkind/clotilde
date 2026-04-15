// Package transcript parses Claude Code JSONL transcripts into structured
// conversation messages. Used by both HTML and plain text export, conversation
// search, and the TUI viewer.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"
)

// Message represents a single parsed conversation turn.
type Message struct {
	UUID      string     // entry UUID (for linking to tool results)
	Role      string     // "user" or "assistant"
	Timestamp time.Time  // when this entry was created
	Text      string     // concatenated text blocks (no tool calls, no thinking)
	Thinking  string     // thinking block text (for HTML export)
	HasTools  bool       // true if assistant message contained tool_use blocks
	Tools     []ToolCall // parsed tool calls with inputs
}

// ToolCall represents a single tool invocation within an assistant message.
type ToolCall struct {
	ID      string         // tool_use_id (links to tool_result in next user message)
	Name    string         // e.g. "Bash", "Edit", "Read"
	Input   map[string]any // tool input parameters
	Output  string         // tool result text (loaded on demand, empty by default)
	IsError bool           // true if tool result was an error
}

// ToolNames returns the names of all tools used in this message.
func (m *Message) ToolNames() []string {
	names := make([]string, len(m.Tools))
	for i, t := range m.Tools {
		names[i] = t.Name
	}
	return names
}

// raw JSON structures for parsing transcript entries
type rawEntry struct {
	UUID      string          `json:"uuid"`
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type     string         `json:"type"`
	Text     string         `json:"text"`
	Thinking string         `json:"thinking"`
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Input    map[string]any `json:"input"`
}

type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"`
	IsError   bool   `json:"is_error"`
}

var systemTagRe = regexp.MustCompile(`<(?:system-reminder|local-command[^>]*|command-name|command-message|command-args|local-command-stdout|local-command-caveat)[^>]*>[\s\S]*?</(?:system-reminder|local-command[^>]*|command-name|command-message|command-args|local-command-stdout|local-command-caveat)>`)

// Parse reads a transcript JSONL file and returns structured messages.
// Tool outputs are NOT loaded by default (expensive). Call LoadToolOutputs
// to populate them for specific messages.
func Parse(r io.Reader) ([]Message, error) {
	reader := bufio.NewReader(r)

	var messages []Message
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > 0 {
				if msg, ok := parseLine(line); ok {
					messages = append(messages, msg)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return messages, err
		}
	}
	return messages, nil
}

func parseLine(line []byte) (Message, bool) {
	var entry rawEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return Message{}, false
	}
	if entry.Type != "user" && entry.Type != "assistant" {
		return Message{}, false
	}
	if len(entry.Message) == 0 {
		return Message{}, false
	}

	var msg rawMessage
	if err := json.Unmarshal(entry.Message, &msg); err != nil {
		return Message{}, false
	}

	m := Message{
		UUID:      entry.UUID,
		Role:      entry.Type,
		Timestamp: entry.Timestamp,
	}

	if entry.Type == "user" {
		text := extractUserText(msg.Content)
		if text == "" {
			return Message{}, false // tool result entry, skip
		}
		m.Text = stripSystemTags(text)
		return m, m.Text != ""
	}

	// Assistant: content is an array of blocks
	parseAssistantBlocks(&m, msg.Content)
	// Include assistant messages even if Text is empty (may have only tool calls)
	return m, m.Text != "" || m.HasTools
}

// extractUserText gets the text from a user message's content field.
// User messages have content as a string (older format) or an array of blocks (newer format).
// Array content may contain text blocks (user-authored) or tool_result blocks (skip those).
func extractUserText(raw json.RawMessage) string {
	// Try string content first (older Claude Code format)
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	// Try array content: extract text blocks, ignore tool_result blocks
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	hasText := false
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				parts = append(parts, t)
				hasText = true
			}
		case "tool_result":
			// tool results are not user-authored text, skip
		}
	}
	if !hasText {
		return "" // only tool results, skip the entry
	}
	return strings.Join(parts, "\n")
}

// parseAssistantBlocks extracts text, thinking, and tool calls from an assistant message.
func parseAssistantBlocks(m *Message, raw json.RawMessage) {
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return
	}

	var textParts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				textParts = append(textParts, t)
			}
		case "thinking":
			if t := strings.TrimSpace(b.Thinking); t != "" {
				m.Thinking = t
			}
		case "tool_use":
			m.HasTools = true
			m.Tools = append(m.Tools, ToolCall{
				ID:    b.ID,
				Name:  b.Name,
				Input: b.Input,
			})
		}
	}
	m.Text = strings.Join(textParts, "\n\n")
}

// stripSystemTags removes system-injected tags from user messages.
func stripSystemTags(s string) string {
	s = systemTagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// LoadToolOutputs populates ToolCall.Output for the given messages by
// scanning the reader for matching tool_result entries. The reader should
// be the same transcript JSONL that was used to Parse the messages.
func LoadToolOutputs(r io.Reader, messages []Message) error {
	// Build index of tool IDs we need
	need := make(map[string]*ToolCall)
	for i := range messages {
		for j := range messages[i].Tools {
			need[messages[i].Tools[j].ID] = &messages[i].Tools[j]
		}
	}
	if len(need) == 0 {
		return nil
	}

	reader := bufio.NewReader(r)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > 0 {
				loadToolResult(line, need)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		// Stop early if all found
		if len(need) == 0 {
			break
		}
	}
	return nil
}

func loadToolResult(line []byte, need map[string]*ToolCall) {
	var entry rawEntry
	if err := json.Unmarshal(line, &entry); err != nil || entry.Type != "user" {
		return
	}
	var msg rawMessage
	if err := json.Unmarshal(entry.Message, &msg); err != nil {
		return
	}

	// Tool results are in user messages with array content
	var blocks []toolResultBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}
	for _, b := range blocks {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		tc, ok := need[b.ToolUseID]
		if !ok {
			continue
		}
		tc.IsError = b.IsError
		switch v := b.Content.(type) {
		case string:
			tc.Output = v
		default:
			if data, err := json.Marshal(v); err == nil {
				tc.Output = string(data)
			}
		}
		delete(need, b.ToolUseID)
	}
}
