package adapter

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
)

const codexApplyPatchToolDescription = "Use the `apply_patch` tool to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON."

const codexShellCommandDescription = "Runs a shell command and returns its output.\n- Always set the `workdir` param when using the shell_command function. Do not use `cd` unless absolutely necessary."

const codexApplyPatchLarkGrammar = `start: begin_patch hunk+ end_patch
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

func codexNativeLocalShellSpec() map[string]any {
	return map[string]any{"type": "local_shell"}
}

func codexShellCommandSpec() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "shell_command",
		"description": codexShellCommandDescription,
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

func codexFunctionToolSpec(name, description string, parameters json.RawMessage, strict *bool) map[string]any {
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

func codexShellToolMode(modelName string) string {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case modelName == "":
		return "shell_command"
	case strings.Contains(modelName, "local-shell"):
		return "local_shell"
	default:
		// Current Codex GPT models advertise ConfigShellToolType::ShellCommand
		// in codex-rs. The direct backend rejects local_shell for gpt-5.4.
		return "shell_command"
	}
}

func codexNativeApplyPatchSpec() map[string]any {
	return map[string]any{
		"type":        "custom",
		"name":        "apply_patch",
		"description": codexApplyPatchToolDescription,
		"format": map[string]any{
			"type":       "grammar",
			"syntax":     "lark",
			"definition": codexApplyPatchLarkGrammar,
		},
	}
}

func codexIsShellToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "Shell", "shell", "local_shell", "shell_command", "container.exec":
		return true
	default:
		return false
	}
}

func codexIsApplyPatchToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "ApplyPatch", "apply_patch":
		return true
	default:
		return false
	}
}

func codexToolCallName(tc ToolCall) string {
	return strings.TrimSpace(tc.Function.Name)
}

func codexToolCallArgsMap(args string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(args)), &out); err != nil {
		return nil
	}
	return out
}

func codexStringArg(args map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, _ := args[key].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func codexNumberArg(args map[string]any, keys ...string) (float64, bool) {
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

func codexLocalShellCallItem(tc ToolCall) codexInputItem {
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = "call_" + strconv.Itoa(tc.Index)
	}
	args := codexToolCallArgsMap(tc.Function.Arguments)
	command := codexStringArg(args, "command", "cmd")
	if command == "" {
		command = strings.TrimSpace(tc.Function.Arguments)
	}
	action := map[string]any{
		"type":    "exec",
		"command": []string{codexShellName(), "-lc", command},
	}
	if cwd := codexStringArg(args, "working_directory", "cwd"); cwd != "" {
		action["working_directory"] = cwd
	}
	if timeout, ok := codexNumberArg(args, "block_until_ms", "timeout_ms"); ok {
		action["timeout_ms"] = timeout
	}
	return codexInputItem{
		"type":    "local_shell_call",
		"call_id": callID,
		"status":  "completed",
		"action":  action,
	}
}

func codexShellCommandCallItem(tc ToolCall) codexInputItem {
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = "call_" + strconv.Itoa(tc.Index)
	}
	args := codexToolCallArgsMap(tc.Function.Arguments)
	command := codexStringArg(args, "command", "cmd")
	if command == "" {
		command = strings.TrimSpace(tc.Function.Arguments)
	}
	payload := map[string]any{"command": command}
	if cwd := codexStringArg(args, "working_directory", "workdir", "cwd"); cwd != "" {
		payload["workdir"] = cwd
	}
	if timeout, ok := codexNumberArg(args, "block_until_ms", "timeout_ms"); ok {
		payload["timeout_ms"] = timeout
	}
	raw, _ := json.Marshal(payload)
	return codexInputItem{
		"type":      "function_call",
		"call_id":   callID,
		"name":      "shell_command",
		"arguments": string(raw),
	}
}

func codexApplyPatchCallItem(tc ToolCall) codexInputItem {
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = "call_" + strconv.Itoa(tc.Index)
	}
	args := codexToolCallArgsMap(tc.Function.Arguments)
	input := codexStringArg(args, "input", "patch")
	if input == "" {
		input = strings.TrimSpace(tc.Function.Arguments)
	}
	input = codexUnwrapApplyPatchInput(input)
	return codexInputItem{
		"type":    "custom_tool_call",
		"call_id": callID,
		"name":    "apply_patch",
		"input":   input,
	}
}

func codexCustomToolCallOutputItem(callID, text string) codexInputItem {
	return codexInputItem{
		"type":    "custom_tool_call_output",
		"call_id": strings.TrimSpace(callID),
		"name":    "apply_patch",
		"output":  text,
	}
}

func codexShellArgsFromLocalShellItem(item map[string]any) (string, bool) {
	action, _ := item["action"].(map[string]any)
	if action == nil {
		return "", false
	}
	command := codexStringSlice(action["command"])
	if len(command) == 0 {
		return "", false
	}
	args := map[string]any{"command": codexCommandString(command)}
	if cwd := codexMapString(action, "working_directory"); cwd != "" {
		args["working_directory"] = cwd
	}
	if timeout, ok := codexNumberFromAny(action["timeout_ms"]); ok {
		args["block_until_ms"] = timeout
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func codexShellArgsFromShellCommandArguments(rawArgs string) (string, bool) {
	args := codexToolCallArgsMap(rawArgs)
	if len(args) == 0 {
		return "", false
	}
	command := codexStringArg(args, "command", "cmd")
	if command == "" {
		return "", false
	}
	out := map[string]any{"command": command}
	if cwd := codexStringArg(args, "working_directory", "workdir", "cwd"); cwd != "" {
		out["working_directory"] = cwd
	}
	if timeout, ok := codexNumberArg(args, "block_until_ms", "timeout_ms", "timeout"); ok {
		out["block_until_ms"] = timeout
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func codexStringSlice(v any) []string {
	switch vals := v.(type) {
	case []any:
		out := make([]string, 0, len(vals))
		for _, val := range vals {
			if s, _ := val.(string); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return vals
	default:
		return nil
	}
}

func codexNumberFromAny(v any) (float64, bool) {
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

func codexCommandString(argv []string) string {
	if len(argv) >= 3 && strings.HasPrefix(filepath.Base(argv[0]), "sh") && (argv[1] == "-lc" || argv[1] == "-c") {
		return strings.Join(argv[2:], " ")
	}
	if len(argv) >= 3 && strings.HasSuffix(filepath.Base(argv[0]), "sh") && (argv[1] == "-lc" || argv[1] == "-c") {
		return strings.Join(argv[2:], " ")
	}
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		parts = append(parts, codexShellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func codexShellQuote(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.IndexFunc(arg, func(r rune) bool {
		return !(r == '_' || r == '-' || r == '.' || r == '/' || r == ':' || r == '=' || r == '+' || r == ',' || r == '%' || r == '@' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
	}) == -1 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

func codexApplyPatchArgs(input string) (string, bool) {
	input = codexUnwrapApplyPatchInput(input)
	if strings.TrimSpace(input) == "" {
		return "", false
	}
	// Cursor's ApplyPatch is a custom/freeform tool. Its input must be the
	// patch text itself, not a JSON object containing the patch.
	return input, true
}

func codexUnwrapApplyPatchInput(input string) string {
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

func codexToolSpecCounts(specs []any) (nativeShell, nativeCustom, function int) {
	for _, spec := range specs {
		m, _ := spec.(map[string]any)
		switch m["type"] {
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
