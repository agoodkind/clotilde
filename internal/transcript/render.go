package transcript

import (
	"fmt"
	"strings"
)

// RenderPlainText formats messages as readable plain text.
// Tool calls are shown as a compact summary line, not full output.
func RenderPlainText(messages []Message) string {
	var b strings.Builder
	for _, m := range messages {
		ts := m.Timestamp.Format("2006-01-02 15:04")
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}

		fmt.Fprintf(&b, "[%s] %s:\n", ts, role)
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
