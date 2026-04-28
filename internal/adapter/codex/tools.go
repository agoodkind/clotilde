package codex

import "strings"

// Cursor sends tool definitions using its own canonical names
// (Shell, ReadFile, ApplyPatch, etc). The Codex websocket expects
// snake-case names (shell, read_file, apply_patch). These helpers
// translate between the two conventions on the request and response
// sides. They live in the codex package because the translation is a
// codex wire concern, not a Cursor product concern.

var cursorToCodexToolName = map[string]string{
	"Shell":            "shell",
	"Glob":             "glob",
	"rg":               "rg",
	"AwaitShell":       "await_shell",
	"ReadFile":         "read_file",
	"Delete":           "delete_file",
	"ApplyPatch":       "apply_patch",
	"EditNotebook":     "edit_notebook",
	"TodoWrite":        "todo_write",
	"ReadLints":        "read_lints",
	"SemanticSearch":   "semantic_search",
	"WebSearch":        "web_search",
	"WebFetch":         "web_fetch",
	"GenerateImage":    "generate_image",
	"AskQuestion":      "ask_question",
	"Subagent":         "spawn_agent",
	"FetchMcpResource": "fetch_mcp_resource",
	"SwitchMode":       "switch_mode",
	"CallMcpTool":      "call_mcp_tool",
	"CreatePlan":       "create_plan",
}

var codexToCursorToolName = func() map[string]string {
	out := make(map[string]string, len(cursorToCodexToolName))
	for cursorName, codexName := range cursorToCodexToolName {
		out[codexName] = cursorName
	}
	return out
}()

// OutboundToolName maps a Cursor canonical tool name to the
// snake-case form the Codex websocket expects on outbound requests.
// Unknown names pass through unchanged.
func OutboundToolName(name string) string {
	name = strings.TrimSpace(name)
	if alias := cursorToCodexToolName[name]; alias != "" {
		return alias
	}
	return name
}

// InboundToolName maps a Codex snake-case tool name back to the
// Cursor canonical form. Unknown names pass through unchanged. The
// special case "shell_command" maps to "Shell".
func InboundToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "shell_command" {
		return "Shell"
	}
	if original := codexToCursorToolName[name]; original != "" {
		return original
	}
	return name
}

// KeepToolForWriteIntent reports whether a tool name is one of the
// Codex-recognised tools and therefore should be kept in the request
// even when the caller's effective tool list does not allow it. This
// preserves write-intent tools (apply_patch, todo_write, etc.) that
// the model expects to remain available.
func KeepToolForWriteIntent(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if cursorToCodexToolName[name] != "" {
		return true
	}
	if codexToCursorToolName[name] != "" {
		return true
	}
	return false
}
