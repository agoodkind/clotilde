package transcript

import (
	"fmt"
	"strings"
)

func RenderPlainTextWithOptions(messages []Message, opts ShapeOptions) string {
	return renderConversationMessages(ShapeConversation(messages, opts), -1)
}

func RenderMarkdownWithOptions(messages []Message, opts ShapeOptions) string {
	return RenderMarkdownConversation(ShapeConversation(messages, opts))
}

func RenderHTMLWithOptions(messages []Message, opts ShapeOptions) string {
	return RenderHTMLConversation(ShapeConversation(messages, opts))
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
