package codex

import "testing"

func TestOutboundToolNamePassesThroughCursorNames(t *testing.T) {
	for _, name := range []string{
		"Read",
		"ReadFile",
		"Write",
		"StrReplace",
		"Task",
		"Subagent",
		"WebSearch",
		"CallMcpTool",
		"custom.tool",
	} {
		if got := OutboundToolName(name); got != name {
			t.Fatalf("outbound %q => %q want passthrough", name, got)
		}
	}
}

func TestInboundToolNamePassesThroughUpstreamNames(t *testing.T) {
	for _, name := range []string{
		"Read",
		"read_file",
		"Write",
		"write_file",
		"StrReplace",
		"str_replace",
		"Task",
		"spawn_agent",
		"shell_command",
		"custom.tool",
	} {
		if got := InboundToolName(name); got != name {
			t.Fatalf("inbound %q => %q want passthrough", name, got)
		}
	}
}
