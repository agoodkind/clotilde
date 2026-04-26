package cursor

import "testing"

func TestCodexToolNameTranslationRoundTrip(t *testing.T) {
	testCases := map[string]string{
		"Shell":            "shell",
		"AwaitShell":       "await_shell",
		"EditNotebook":     "edit_notebook",
		"TodoWrite":        "todo_write",
		"ReadLints":        "read_lints",
		"SemanticSearch":   "semantic_search",
		"Subagent":         "spawn_agent",
		"FetchMcpResource": "fetch_mcp_resource",
		"SwitchMode":       "switch_mode",
		"CallMcpTool":      "call_mcp_tool",
		"CreatePlan":       "create_plan",
	}

	for cursorName, codexName := range testCases {
		if got := OutboundCodexToolName(cursorName); got != codexName {
			t.Fatalf("outbound %q => %q want %q", cursorName, got, codexName)
		}
		if got := InboundCodexToolName(codexName); got != cursorName {
			t.Fatalf("inbound %q => %q want %q", codexName, got, cursorName)
		}
	}
}

func TestInboundCodexToolNameNormalizesNativeShellTool(t *testing.T) {
	if got := InboundCodexToolName("shell_command"); got != "Shell" {
		t.Fatalf("inbound shell_command => %q want %q", got, "Shell")
	}
}
