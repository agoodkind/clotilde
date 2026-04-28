package codex

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
		if got := OutboundToolName(cursorName); got != codexName {
			t.Fatalf("outbound %q => %q want %q", cursorName, got, codexName)
		}
		if got := InboundToolName(codexName); got != cursorName {
			t.Fatalf("inbound %q => %q want %q", codexName, got, cursorName)
		}
	}
}

func TestInboundToolNameNormalizesNativeShellTool(t *testing.T) {
	if got := InboundToolName("shell_command"); got != "Shell" {
		t.Fatalf("inbound shell_command => %q want %q", got, "Shell")
	}
}

func TestKeepToolForWriteIntent(t *testing.T) {
	if !KeepToolForWriteIntent("ApplyPatch") {
		t.Errorf("KeepToolForWriteIntent(ApplyPatch) = false, want true")
	}
	if !KeepToolForWriteIntent("apply_patch") {
		t.Errorf("KeepToolForWriteIntent(apply_patch) = false, want true")
	}
	if KeepToolForWriteIntent("") {
		t.Errorf("KeepToolForWriteIntent(\"\") = true, want false")
	}
	if KeepToolForWriteIntent("nonsense") {
		t.Errorf("KeepToolForWriteIntent(nonsense) = true, want false")
	}
}
