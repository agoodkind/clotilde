package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

const ApplyPatchToolDescription = "Use the `apply_patch` tool to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON."

const ShellCommandDescription = "Runs a shell command and returns its output.\n- Always set the `workdir` param when using the shell_command function. Do not use `cd` unless absolutely necessary."

const ApplyPatchLarkGrammar = `start: begin_patch hunk+ end_patch
begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?

hunk: add_hunk | delete_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
delete_hunk: "*** Delete File: " filename LF
update_hunk: "*** Update File: " filename LF change_move? change?

filename: /(.+)/
add_line: "+" /(.*)/ LF -> line

change_move: "*** Move to: " filename LF
change: (change_context | change_line)+ eof_line?
change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF

%import common.LF
`

func NativeLocalShellSpec() map[string]any {
	return map[string]any{"type": "local_shell"}
}

func ShellCommandSpec() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "shell_command",
		"description": ShellCommandDescription,
		"strict":      false,
		"parameters": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"command"},
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell script to execute in the user's default shell",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "The working directory to execute the command in",
				},
				"timeout_ms": map[string]any{
					"type":        "number",
					"description": "The timeout for the command in milliseconds",
				},
			},
		},
	}
}

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

func ShellToolMode(modelName string) string {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.HasPrefix(modelName, "gpt-"):
		return "shell_command"
	case strings.Contains(modelName, "shell-command"):
		return "shell_command"
	default:
		return "local_shell"
	}
}

func ShellToolModeForModel(model adaptermodel.ResolvedModel) string {
	modelName := strings.TrimSpace(model.ClaudeModel)
	if modelName == "" {
		modelName = model.Alias
	}
	return ShellToolMode(modelName)
}

func NativeApplyPatchSpec() map[string]any {
	return map[string]any{
		"type":        "custom",
		"name":        "apply_patch",
		"description": ApplyPatchToolDescription,
		"format": map[string]any{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": ApplyPatchLarkGrammar,
		},
	}
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

func ToolCallName(tc adapteropenai.ToolCall) string {
	return strings.TrimSpace(tc.Function.Name)
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

func LocalShellCallItem(tc adapteropenai.ToolCall, shellName string) map[string]any {
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = "call_" + strconv.Itoa(tc.Index)
	}
	args := ToolCallArgsMap(tc.Function.Arguments)
	command := StringArg(args, "command", "cmd")
	if command == "" {
		command = strings.TrimSpace(tc.Function.Arguments)
	}
	if strings.TrimSpace(shellName) == "" {
		shellName = detectedShellName()
	}
	action := map[string]any{
		"type":    "exec",
		"command": []string{shellName, "-lc", command},
	}
	if cwd := StringArg(args, "working_directory", "cwd"); cwd != "" {
		action["working_directory"] = cwd
	}
	if timeout, ok := NumberArg(args, "block_until_ms", "timeout_ms"); ok {
		action["timeout_ms"] = timeout
	}
	return map[string]any{
		"type":    "local_shell_call",
		"call_id": callID,
		"status":  "completed",
		"action":  action,
	}
}

func ShellCommandCallItem(tc adapteropenai.ToolCall) map[string]any {
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = "call_" + strconv.Itoa(tc.Index)
	}
	args := ToolCallArgsMap(tc.Function.Arguments)
	command := StringArg(args, "command", "cmd")
	if command == "" {
		command = strings.TrimSpace(tc.Function.Arguments)
	}
	payload := map[string]any{"command": command}
	if cwd := StringArg(args, "working_directory", "workdir", "cwd"); cwd != "" {
		payload["workdir"] = cwd
	}
	if timeout, ok := NumberArg(args, "block_until_ms", "timeout_ms"); ok {
		payload["timeout_ms"] = timeout
	}
	raw, _ := json.Marshal(payload)
	return map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      "shell_command",
		"arguments": string(raw),
	}
}

func ApplyPatchCallItem(tc adapteropenai.ToolCall) map[string]any {
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = "call_" + strconv.Itoa(tc.Index)
	}
	args := ToolCallArgsMap(tc.Function.Arguments)
	input := StringArg(args, "input", "patch")
	if input == "" {
		input = strings.TrimSpace(tc.Function.Arguments)
	}
	input = UnwrapApplyPatchInput(input)
	return map[string]any{
		"type":    "custom_tool_call",
		"call_id": callID,
		"name":    "apply_patch",
		"input":   input,
	}
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

func ToolSpecCounts(specs []any) (nativeShell, nativeCustom, function int) {
	for _, spec := range specs {
		m, _ := spec.(map[string]any)
		switch strings.TrimSpace(StringArg(m, "type")) {
		case "local_shell":
			nativeShell++
		case "custom":
			nativeCustom++
		case "function":
			function++
		}
	}
	return nativeShell, nativeCustom, function
}

func detectedShellName() string {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		return "sh"
	}
	parts := strings.Split(shell, "/")
	return parts[len(parts)-1]
}
