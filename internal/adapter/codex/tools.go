package codex

import "strings"

// OutboundToolName preserves the caller-provided function tool name.
func OutboundToolName(name string) string {
	return strings.TrimSpace(name)
}

// InboundToolName preserves the upstream-provided function tool name.
func InboundToolName(name string) string {
	return strings.TrimSpace(name)
}
