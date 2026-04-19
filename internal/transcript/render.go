package transcript

import (
	"fmt"
	"strings"
)

// RenderPlainText formats messages as readable plain text.
// Tool calls are shown as a compact summary line, not full output.
func RenderPlainText(messages []Message) string {
	return renderMessages(messages, -1)
}

func renderMessages(messages []Message, startIndex int) string {
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
		if m.HasTools {
			names := m.ToolNames()
			fmt.Fprintf(&b, "  [used: %s]\n", strings.Join(names, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}
