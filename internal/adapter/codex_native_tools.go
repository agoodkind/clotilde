package adapter

import (
	"encoding/json"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
)

const codexApplyPatchToolDescription = adaptercodex.ApplyPatchToolDescription
const codexShellCommandDescription = adaptercodex.ShellCommandDescription
const codexApplyPatchLarkGrammar = adaptercodex.ApplyPatchLarkGrammar

func codexNativeLocalShellSpec() map[string]any { return adaptercodex.NativeLocalShellSpec() }
func codexShellCommandSpec() map[string]any     { return adaptercodex.ShellCommandSpec() }
func codexFunctionToolSpec(name, description string, parameters json.RawMessage, strict *bool) map[string]any {
	return adaptercodex.FunctionToolSpec(name, description, parameters, strict)
}
func codexShellToolMode(modelName string) string      { return adaptercodex.ShellToolMode(modelName) }
func codexNativeApplyPatchSpec() map[string]any       { return adaptercodex.NativeApplyPatchSpec() }
func codexIsShellToolName(name string) bool           { return adaptercodex.IsShellToolName(name) }
func codexIsApplyPatchToolName(name string) bool      { return adaptercodex.IsApplyPatchToolName(name) }
func codexToolCallName(tc ToolCall) string            { return adaptercodex.ToolCallName(tc) }
func codexToolCallArgsMap(args string) map[string]any { return adaptercodex.ToolCallArgsMap(args) }
func codexStringArg(args map[string]any, keys ...string) string {
	return adaptercodex.StringArg(args, keys...)
}
func codexNumberArg(args map[string]any, keys ...string) (float64, bool) {
	return adaptercodex.NumberArg(args, keys...)
}
func codexLocalShellCallItem(tc ToolCall) codexInputItem {
	return codexInputItem(adaptercodex.LocalShellCallItem(tc, codexShellName()))
}
func codexShellCommandCallItem(tc ToolCall) codexInputItem {
	return codexInputItem(adaptercodex.ShellCommandCallItem(tc))
}
func codexApplyPatchCallItem(tc ToolCall) codexInputItem {
	return codexInputItem(adaptercodex.ApplyPatchCallItem(tc))
}
func codexCustomToolCallOutputItem(callID, text string) codexInputItem {
	return codexInputItem(adaptercodex.CustomToolCallOutputItem(callID, text))
}
func codexShellArgsFromLocalShellItem(item map[string]any) (string, bool) {
	return adaptercodex.ShellArgsFromLocalShellItem(item)
}
func codexShellArgsFromShellCommandArguments(rawArgs string) (string, bool) {
	return adaptercodex.ShellArgsFromShellCommandArguments(rawArgs)
}
func codexStringSlice(v any) []string                 { return adaptercodex.StringSlice(v) }
func codexNumberFromAny(v any) (float64, bool)        { return adaptercodex.NumberFromAny(v) }
func codexCommandString(argv []string) string         { return adaptercodex.CommandString(argv) }
func codexShellQuote(arg string) string               { return adaptercodex.ShellQuote(arg) }
func codexApplyPatchArgs(input string) (string, bool) { return adaptercodex.ApplyPatchArgs(input) }
func codexUnwrapApplyPatchInput(input string) string {
	return adaptercodex.UnwrapApplyPatchInput(input)
}
func codexToolSpecCounts(specs []any) (int, int, int) { return adaptercodex.ToolSpecCounts(specs) }
