package transcript

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

type ToolOnlyMode string

const (
	ToolOnlyOmit           ToolOnlyMode = "omit"
	ToolOnlyCompactSummary ToolOnlyMode = "compact_summary"
	ToolOnlyFullDetail     ToolOnlyMode = "full_detail"
)

type ShapeOptions struct {
	IncludeThinking  bool
	ConversationOnly bool
	ToolOnly         ToolOnlyMode
	MaxTextRunes     int
}

var (
	conversationOnlyExactDrops = map[string]bool{
		"No response requested.":                     true,
		"[Request interrupted by user]":              true,
		"[Request interrupted by user for tool use]": true,
	}
	conversationOnlyImageLineRe = regexp.MustCompile(`^\[Image(?::| #).*\]$`)
)

type ConversationTurn struct {
	UUID       string    `json:"uuid,omitempty"`
	Role       string    `json:"role"`
	Timestamp  time.Time `json:"timestamp,omitzero"`
	Text       string    `json:"text"`
	Thinking   string    `json:"thinking,omitempty"`
	ToolNames  []string  `json:"tool_names,omitempty"`
	HasTools   bool      `json:"has_tools,omitempty"`
	IsToolOnly bool      `json:"is_tool_only,omitempty"`
}

func DefaultShapeOptions() ShapeOptions {
	return ShapeOptions{
		ToolOnly: ToolOnlyCompactSummary,
	}
}

func ShapeConversation(messages []Message, opts ShapeOptions) []ConversationTurn {
	if opts.ToolOnly == "" {
		opts.ToolOnly = ToolOnlyCompactSummary
	}
	out := make([]ConversationTurn, 0, len(messages))
	for _, msg := range messages {
		turn := ConversationTurn{
			UUID:      msg.UUID,
			Role:      msg.Role,
			Timestamp: msg.Timestamp,
			HasTools:  msg.HasTools,
		}
		text := normalizeConversationText(msg.Text, opts.MaxTextRunes, opts.ConversationOnly)
		thinking := ""
		if opts.IncludeThinking {
			thinking = normalizeConversationText(msg.Thinking, opts.MaxTextRunes, false)
		}
		if len(msg.Tools) > 0 {
			turn.ToolNames = msg.ToolNames()
		}
		if text == "" && msg.HasTools {
			turn.IsToolOnly = true
			if opts.ConversationOnly {
				continue
			}
			switch opts.ToolOnly {
			case ToolOnlyOmit:
				continue
			case ToolOnlyCompactSummary:
				text = toolSummaryText(turn.ToolNames)
			case ToolOnlyFullDetail:
				text = toolFullDetailText(msg.Tools)
			default:
				text = toolSummaryText(turn.ToolNames)
			}
		}
		if text == "" && thinking == "" {
			continue
		}
		turn.Text = text
		turn.Thinking = thinking
		out = append(out, turn)
	}
	return out
}

func normalizeConversationText(text string, maxRunes int, conversationOnly bool) string {
	text = strings.ReplaceAll(text, "\r", "")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	lastBlank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if conversationOnly && shouldDropConversationOnlyLine(line) {
			continue
		}
		if line == "" {
			if lastBlank {
				continue
			}
			out = append(out, "")
			lastBlank = true
			continue
		}
		out = append(out, line)
		lastBlank = false
	}
	text = strings.TrimSpace(strings.Join(out, "\n"))
	if text == "" {
		return ""
	}
	if maxRunes > 0 {
		runes := []rune(text)
		if len(runes) > maxRunes {
			text = string(runes[:maxRunes]) + "..."
		}
	}
	return text
}

func shouldDropConversationOnlyLine(line string) bool {
	if line == "" {
		return false
	}
	if _, ok := conversationOnlyExactDrops[line]; ok {
		return true
	}
	return conversationOnlyImageLineRe.MatchString(line)
}

func toolSummaryText(names []string) string {
	if len(names) == 0 {
		return "[used tools]"
	}
	return "[used: " + strings.Join(names, ", ") + "]"
}

func toolFullDetailText(tools []ToolCall) string {
	if len(tools) == 0 {
		return "[used tools]"
	}
	lines := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Input.Len() == 0 {
			lines = append(lines, "[tool: "+tool.Name+"]")
			continue
		}
		body, _ := json.Marshal(tool.Input)
		line := fmt.Sprintf("[tool: %s] %s", tool.Name, string(body))
		if strings.TrimSpace(tool.Output) != "" {
			line += "\n[tool output]\n" + strings.TrimSpace(tool.Output)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func RenderMarkdownConversation(turns []ConversationTurn) string {
	var b strings.Builder
	for _, turn := range turns {
		role := "User"
		if turn.Role == "assistant" {
			role = "Assistant"
		}
		if !turn.Timestamp.IsZero() {
			fmt.Fprintf(&b, "### %s (%s)\n\n", role, turn.Timestamp.Format("2006-01-02 15:04"))
		} else {
			fmt.Fprintf(&b, "### %s\n\n", role)
		}
		if turn.Text != "" {
			b.WriteString(turn.Text)
			b.WriteString("\n\n")
		}
		if turn.Thinking != "" {
			b.WriteString("> Thinking\n>\n")
			for line := range strings.SplitSeq(turn.Thinking, "\n") {
				b.WriteString("> " + line + "\n")
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func RenderHTMLConversation(turns []ConversationTurn) string {
	var b strings.Builder
	b.WriteString("<div class=\"conversation\">\n")
	for _, turn := range turns {
		role := "User"
		if turn.Role == "assistant" {
			role = "Assistant"
		}
		b.WriteString("<section class=\"turn\">\n")
		b.WriteString("<header><strong>" + html.EscapeString(role) + "</strong>")
		if !turn.Timestamp.IsZero() {
			b.WriteString(" <time>" + html.EscapeString(turn.Timestamp.Format("2006-01-02 15:04")) + "</time>")
		}
		b.WriteString("</header>\n")
		if turn.Text != "" {
			b.WriteString("<p>" + html.EscapeString(turn.Text) + "</p>\n")
		}
		if turn.Thinking != "" {
			b.WriteString("<blockquote><strong>Thinking</strong><br>" + strings.ReplaceAll(html.EscapeString(turn.Thinking), "\n", "<br>") + "</blockquote>\n")
		}
		b.WriteString("</section>\n")
	}
	b.WriteString("</div>")
	return b.String()
}

func RenderJSONConversation(turns []ConversationTurn) ([]byte, error) {
	return json.MarshalIndent(turns, "", "  ")
}
