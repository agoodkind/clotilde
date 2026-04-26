package cursor

import "strings"

var codexToolNameAliases = map[string]string{
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

var codexToolNameReverseAliases = func() map[string]string {
	out := make(map[string]string, len(codexToolNameAliases))
	for cursorName, codexName := range codexToolNameAliases {
		out[codexName] = cursorName
	}
	return out
}()

func OutboundCodexToolName(name string) string {
	name = strings.TrimSpace(name)
	if alias := codexToolNameAliases[name]; alias != "" {
		return alias
	}
	return name
}

func InboundCodexToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "shell_command" {
		return "Shell"
	}
	if original := codexToolNameReverseAliases[name]; original != "" {
		return original
	}
	return name
}

func KeepCodexToolForWriteIntent(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if codexToolNameAliases[name] != "" {
		return true
	}
	if codexToolNameReverseAliases[name] != "" {
		return true
	}
	return false
}
