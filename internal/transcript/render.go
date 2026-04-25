package transcript

import (
	"fmt"
	"strings"
)

// RenderPlainText formats messages as readable plain text.
// Tool-only assistant turns render as compact summaries by default.
func RenderPlainText(messages []Message) string {
	return RenderPlainTextWithOptions(messages, DefaultShapeOptions())
}

func RenderPlainTextWithOptions(messages []Message, opts ShapeOptions) string {
	return renderConversationMessages(ShapeConversation(messages, opts), -1)
}

func RenderMarkdown(messages []Message) string {
	return RenderMarkdownWithOptions(messages, DefaultShapeOptions())
}

func RenderMarkdownWithOptions(messages []Message, opts ShapeOptions) string {
	return RenderMarkdownConversation(ShapeConversation(messages, opts))
}

func RenderHTML(messages []Message) string {
	return RenderHTMLWithOptions(messages, DefaultShapeOptions())
}

func RenderHTMLWithOptions(messages []Message, opts ShapeOptions) string {
	return RenderHTMLConversation(ShapeConversation(messages, opts))
}

func RenderJSON(messages []Message) ([]byte, error) {
	return RenderJSONWithOptions(messages, DefaultShapeOptions())
}

func RenderJSONWithOptions(messages []Message, opts ShapeOptions) ([]byte, error) {
	return RenderJSONConversation(ShapeConversation(messages, opts))
}

func renderConversationMessages(messages []ConversationTurn, startIndex int) string {
	var b strings.Builder
	for i, m := range messages {
		ts := m.Timestamp.Format("2006-01-02 15:04")
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}

		if startIndex >= 0 {
			fmt.Fprintf(&b, "[#%d][%s] %s:\n", startIndex+i, ts, role)
		} else {
			fmt.Fprintf(&b, "[%s] %s:\n", ts, role)
		}
		if m.Text != "" {
			b.WriteString(m.Text)
			b.WriteString("\n")
		}
		if m.Thinking != "" {
			b.WriteString("[thinking]\n")
			b.WriteString(m.Thinking)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
