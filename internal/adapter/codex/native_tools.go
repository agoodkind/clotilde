package codex

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func FunctionToolSpec(name, description string, parameters json.RawMessage, strict *bool) map[string]any {
	spec := map[string]any{
		"type": "function",
		"name": strings.TrimSpace(name),
	}
	if desc := strings.TrimSpace(description); desc != "" {
		spec["description"] = desc
	}
	if len(parameters) > 0 && string(parameters) != "null" {
		var params any
		if err := json.Unmarshal(parameters, &params); err == nil {
			spec["parameters"] = params
		}
	}
	if strict != nil {
		spec["strict"] = *strict
	}
	return spec
}

func IsShellToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "Shell", "shell", "local_shell", "shell_command", "container.exec":
		return true
	default:
		return false
	}
}

func IsApplyPatchToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "ApplyPatch", "apply_patch":
		return true
	default:
		return false
	}
}

func ToolCallArgsMap(args string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &out); err != nil {
		return nil
	}
	return out
}

func StringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, _ := args[key].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func NumberArg(args map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := args[key].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case json.Number:
			f, err := v.Float64()
			return f, err == nil
		}
	}
	return 0, false
}

func CustomToolCallOutputItem(callID, text string) map[string]any {
	return map[string]any{
		"type":    "custom_tool_call_output",
		"call_id": strings.TrimSpace(callID),
		"name":    "apply_patch",
		"output":  text,
	}
}

func ShellArgsFromLocalShellItem(item map[string]any) (string, bool) {
	action, _ := item["action"].(map[string]any)
	if action == nil {
		return "", false
	}
	command := StringSlice(action["command"])
	if len(command) == 0 {
		return "", false
	}
	args := map[string]any{"command": CommandString(command)}
	if cwd, _ := action["working_directory"].(string); strings.TrimSpace(cwd) != "" {
		args["working_directory"] = strings.TrimSpace(cwd)
	}
	if timeout, ok := NumberFromAny(action["timeout_ms"]); ok {
		args["block_until_ms"] = int(timeout)
	}
	raw, _ := json.Marshal(args)
	return string(raw), true
}

func ShellArgsFromShellCommandArguments(rawArgs string) (string, bool) {
	args := ToolCallArgsMap(rawArgs)
	if len(args) == 0 {
		return "", false
	}
	command := StringArg(args, "command", "cmd")
	if command == "" {
		return "", false
	}
	out := map[string]any{"command": command}
	if cwd := StringArg(args, "working_directory", "workdir", "cwd"); cwd != "" {
		out["working_directory"] = cwd
	}
	if timeout, ok := NumberArg(args, "block_until_ms", "timeout_ms", "timeout"); ok {
		out["block_until_ms"] = int(timeout)
	}
	raw, _ := json.Marshal(out)
	return string(raw), true
}

func StringSlice(v any) []string {
	raw, _ := v.([]any)
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, _ := item.(string); strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func NumberFromAny(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func CommandString(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	if len(argv) >= 3 && strings.HasSuffix(filepath.Base(argv[0]), "sh") && (argv[1] == "-lc" || argv[1] == "-c") {
		return strings.Join(argv[2:], " ")
	}
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, ShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func ShellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool {
		if r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == '+' || r == ',' || r == '%' || r == '@' {
			return false
		}
		if r >= '0' && r <= '9' {
			return false
		}
		if r >= 'A' && r <= 'Z' {
			return false
		}
		return r < 'a' || r > 'z'
	}) == -1 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func ApplyPatchArgs(input string) (string, bool) {
	input = UnwrapApplyPatchInput(input)
	input = RepairApplyPatchInput(input)
	if strings.TrimSpace(input) == "" {
		return "", false
	}
	return input, true
}

func UnwrapApplyPatchInput(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	var obj map[string]string
	trimmed := strings.TrimSpace(input)
	if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
		if v := obj["input"]; strings.TrimSpace(v) != "" {
			return v
		}
		if v := obj["patch"]; strings.TrimSpace(v) != "" {
			return v
		}
	}
	return input
}

func RepairApplyPatchInput(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	lines := strings.SplitAfter(input, "\n")
	if len(lines) == 0 {
		return input
	}
	out := make([]string, 0, len(lines))
	for i := range lines {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "*** Update File: ") && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if startsPatchHunkHeader(next) {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "")
}

func startsPatchHunkHeader(line string) bool {
	return strings.HasPrefix(line, "*** Add File: ") ||
		strings.HasPrefix(line, "*** Delete File: ") ||
		strings.HasPrefix(line, "*** Update File: ")
}
