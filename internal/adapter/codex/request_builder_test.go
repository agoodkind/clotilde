package codex

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
)

type ChatRequest = adapteropenai.ChatRequest
type ChatMessage = adapteropenai.ChatMessage
type Tool = adapteropenai.Tool
type ToolFunctionSchema = adapteropenai.ToolFunctionSchema
type ToolCall = adapteropenai.ToolCall
type ToolCallFunction = adapteropenai.ToolCallFunction
type ResolvedModel = adaptermodel.ResolvedModel
type codexInputItem = map[string]any

const (
	EffortMedium = adaptermodel.EffortMedium
	EffortHigh   = adaptermodel.EffortHigh
	EffortXHigh  = adaptermodel.EffortXHigh
)

func mustRaw(s string) []byte {
	return []byte(s)
}

func buildManagedPromptPlanForTest(messages []ChatMessage) ManagedPromptPlan {
	return BuildManagedPromptPlan(messages, buildPromptForTest, adapteropenai.FlattenContent, SanitizeForUpstreamCache)
}

func buildPromptForTest(messages []ChatMessage) (system, prompt string) {
	var sys []string
	var body []string
	for _, m := range messages {
		text := adapteropenai.FlattenContent(m.Content)
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			if text != "" {
				sys = append(sys, text)
			}
		case "user":
			body = append(body, "user: "+text)
		case "assistant":
			body = append(body, "assistant: "+text)
		case "tool":
			body = append(body, "tool: "+text)
		default:
			body = append(body, m.Role+": "+text)
		}
	}
	return strings.Join(sys, "\n\n"), strings.Join(body, "\n\n")
}

func TestBuildCodexRequestIncludesReasoningEffort(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, EffortMedium)
	if out.Reasoning == nil {
		t.Fatalf("expected reasoning stanza")
	}
	if out.Reasoning.Effort != EffortMedium {
		t.Fatalf("reasoning.effort=%q want %q", out.Reasoning.Effort, EffortMedium)
	}
}

func TestBuildCodexRequestUsesNormalizedUpstreamModel(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{
		Alias:       "clyde-gpt-5.4",
		ClaudeModel: "gpt-5.4",
	}

	out := BuildRequest(req, model, "")
	if out.Model != "gpt-5.4" {
		t.Fatalf("model=%q want gpt-5.4", out.Model)
	}
}

func TestBuildCodexRequestUsesSparkModelSlug(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{
		Alias:       "clyde-gpt-5.3-codex-spark",
		ClaudeModel: "gpt-5.3-codex-spark",
	}

	out := BuildRequest(req, model, "")
	if out.Model != "gpt-5.3-codex-spark" {
		t.Fatalf("model=%q want gpt-5.3-codex-spark", out.Model)
	}
}

func TestBuildCodexRequestUsesNativeModelAndRequestEffort(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{
		Alias:       "gpt-5.4",
		ClaudeModel: "gpt-5.4",
	}

	out := BuildRequest(req, model, EffortXHigh)
	if out.Model != "gpt-5.4" {
		t.Fatalf("model=%q want gpt-5.4", out.Model)
	}
	if out.Reasoning == nil || out.Reasoning.Effort != EffortXHigh {
		t.Fatalf("reasoning = %+v want effort %q", out.Reasoning, EffortXHigh)
	}
}

func TestBuildCodexRequestFallsBackToRequestReasoningEffort(t *testing.T) {
	req := ChatRequest{
		ReasoningEffort: "high",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, "")
	if out.Reasoning == nil || out.Reasoning.Effort != EffortHigh {
		t.Fatalf("reasoning fallback failed: %+v", out.Reasoning)
	}
}

func TestBuildCodexRequestSkipsInvalidReasoningEffort(t *testing.T) {
	req := ChatRequest{
		ReasoningEffort: "max",
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, "")
	if out.Reasoning != nil {
		t.Fatalf("expected no reasoning stanza for invalid effort, got %+v", out.Reasoning)
	}
}

func TestBuildCodexRequestUsesResponsesReasoningFields(t *testing.T) {
	req := ChatRequest{
		Include: []string{"reasoning.encrypted_content"},
		Reasoning: &adapteropenai.Reasoning{
			Effort:  "medium",
			Summary: "auto",
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, "")
	if out.Reasoning == nil {
		t.Fatalf("expected reasoning stanza")
	}
	if out.Reasoning.Effort != EffortMedium {
		t.Fatalf("effort=%q want %q", out.Reasoning.Effort, EffortMedium)
	}
	if out.Reasoning.Summary != "auto" {
		t.Fatalf("summary=%q want auto", out.Reasoning.Summary)
	}
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%v", out.Include)
	}
}

func TestBuildCodexRequestPassesThroughMaxCompletionTokens(t *testing.T) {
	maxCompletion := 4096
	req := ChatRequest{
		MaxComplTokens: &maxCompletion,
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	if out.MaxCompletion == nil {
		t.Fatalf("expected max_completion_tokens passthrough")
	}
	if *out.MaxCompletion != maxCompletion {
		t.Fatalf("max_completion_tokens=%d want %d", *out.MaxCompletion, maxCompletion)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["max_completion_tokens"].(float64); int(got) != maxCompletion {
		t.Fatalf("serialized max_completion_tokens=%v want %d", payload["max_completion_tokens"], maxCompletion)
	}
}

func TestBuildCodexRequestMapsFastServiceTierToPriority(t *testing.T) {
	req := ChatRequest{
		Metadata: mustRaw(`{"service_tier":"fast"}`),
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	if out.ServiceTier != "priority" {
		t.Fatalf("service_tier=%q want priority", out.ServiceTier)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["service_tier"].(string); got != "priority" {
		t.Fatalf("serialized service_tier=%q want priority", got)
	}
}

func TestBuildCodexRequestPreservesFlexServiceTier(t *testing.T) {
	req := ChatRequest{
		Metadata: mustRaw(`{"service_tier":"flex"}`),
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	if out.ServiceTier != "flex" {
		t.Fatalf("service_tier=%q want flex", out.ServiceTier)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["service_tier"].(string); got != "flex" {
		t.Fatalf("serialized service_tier=%q want flex", got)
	}
}

func TestBuildCodexRequestReplaysAssistantTurnsAsOutputText(t *testing.T) {
	oldNow := NowFunc
	oldGetwd := GetwdFn
	oldShell := ShellNameFn
	NowFunc = func() time.Time { return time.Date(2026, 4, 25, 9, 0, 0, 0, time.FixedZone("PDT", -7*3600)) }
	GetwdFn = func() (string, error) { return "/repo", nil }
	ShellNameFn = func() string { return "zsh" }
	defer func() {
		NowFunc = oldNow
		GetwdFn = oldGetwd
		ShellNameFn = oldShell
	}()

	req := ChatRequest{
		Messages: []ChatMessage{
			{
				Role:    "user",
				Content: json.RawMessage(`"question"`),
			},
			{
				Role:    "assistant",
				Content: json.RawMessage(`"answer"`),
			},
		},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, "")
	foundOutput := false
	foundPermissions := false
	foundEnvironment := false
	for _, item := range out.Input {
		switch {
		case codexInputContentType(item) == "output_text":
			foundOutput = true
		case strings.Contains(codexInputContentText(item), "<permissions instructions>"):
			foundPermissions = true
		case strings.Contains(codexInputContentText(item), "<environment_context>"):
			foundEnvironment = true
		}
	}
	if !foundOutput {
		t.Fatalf("expected assistant output_text in %#v", out.Input)
	}
	if !foundPermissions {
		t.Fatalf("expected permissions context in %#v", out.Input)
	}
	if !foundEnvironment {
		t.Fatalf("expected environment context in %#v", out.Input)
	}
}

func TestBuildCodexRequestInjectsContextBeforeFinalUserTurn(t *testing.T) {
	oldNow := NowFunc
	oldGetwd := GetwdFn
	oldShell := ShellNameFn
	NowFunc = func() time.Time { return time.Date(2026, 4, 25, 9, 0, 0, 0, time.FixedZone("PDT", -7*3600)) }
	GetwdFn = func() (string, error) { return "/repo", nil }
	ShellNameFn = func() string { return "zsh" }
	defer func() {
		NowFunc = oldNow
		GetwdFn = oldGetwd
		ShellNameFn = oldShell
	}()

	req := ChatRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: mustRaw(`"system rules"`)},
			{Role: "user", Content: mustRaw(`"first"`)},
			{Role: "assistant", Content: mustRaw(`"second"`)},
			{Role: "user", Content: mustRaw(`"write the file"`)},
		},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	if got, _ := out.Input[0]["role"].(string); got != "user" {
		t.Fatalf("role[0]=%q want user", got)
	}
	if got, _ := out.Input[1]["role"].(string); got != "assistant" {
		t.Fatalf("role[1]=%q want assistant", got)
	}
	if got, _ := out.Input[2]["role"].(string); got != "developer" {
		t.Fatalf("role[2]=%q want developer", got)
	}
	if got, _ := out.Input[3]["role"].(string); got != "user" {
		t.Fatalf("role[3]=%q want user env", got)
	}
	if got, _ := out.Input[4]["role"].(string); got != "user" {
		t.Fatalf("role[5]=%q want final user", got)
	}
	devContent := codexInputContentText(out.Input[2])
	if !strings.Contains(devContent, "<permissions instructions>") || !strings.Contains(devContent, "<tool_calling_instructions>") || !strings.Contains(devContent, "system rules") {
		t.Fatalf("developer_context=%q", devContent)
	}
	content := codexInputContentText(out.Input[3])
	if !strings.Contains(content, "<environment_context>") || !strings.Contains(content, "<cwd>/repo</cwd>") {
		t.Fatalf("environment_context=%q", content)
	}
}

func TestBuildCodexRequestUsesCatalogBaseInstructions(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{{Role: "user", Content: mustRaw(`"hello"`)}},
	}
	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	if !strings.Contains(out.Instructions, "Agent Mode") {
		t.Fatalf("instructions missing agent mode prefix")
	}
	if !strings.Contains(out.Instructions, "You are Codex, a coding agent") {
		t.Fatalf("instructions did not come from catalog")
	}
}

func TestBuildCodexRequestUsesPlanModeInstructionPrefix(t *testing.T) {
	req := ChatRequest{
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunctionSchema{
				Name: "CreatePlan",
			},
		}},
		Messages: []ChatMessage{{Role: "user", Content: mustRaw(`"draft the plan"`)}},
	}
	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	if !strings.Contains(out.Instructions, "Plan Mode") {
		t.Fatalf("instructions missing plan mode prefix: %q", out.Instructions)
	}
	if !strings.Contains(out.Instructions, "Only edit markdown files") {
		t.Fatalf("instructions missing plan mode guardrail: %q", out.Instructions)
	}
}

func TestBuildCodexRequestIncludesToolsAndParallelToolCalls(t *testing.T) {
	parallel := true
	req := ChatRequest{
		ParallelTools: &parallel,
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunctionSchema{
				Name:        "write_file",
				Description: "Write a file.",
				Parameters:  mustRaw(`{"type":"object","properties":{"path":{"type":"string"}}}`),
			},
		}},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: mustRaw(`"write it"`),
		}},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	if len(out.Tools) != 1 {
		t.Fatalf("tools len=%d want 1", len(out.Tools))
	}
	tool, ok := out.Tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool type=%T", out.Tools[0])
	}
	if tool["type"] != "function" || tool["name"] != "write_file" {
		t.Fatalf("tool=%v", tool)
	}
	if out.ToolChoice != "auto" {
		t.Fatalf("tool_choice=%q want auto", out.ToolChoice)
	}
	if !out.ParallelToolCalls {
		t.Fatalf("parallel_tool_calls=false want true")
	}
}

func TestBuildCodexRequestMapsCursorToolNamesToCodexAliases(t *testing.T) {
	req := ChatRequest{
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunctionSchema{
				Name:        "ReadFile",
				Description: "Read a file.",
				Parameters:  mustRaw(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
			},
		}},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: mustRaw(`"read it"`),
		}},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	tool, ok := out.Tools[0].(map[string]any)
	if !ok {
		t.Fatalf("tool type=%T", out.Tools[0])
	}
	if tool["name"] != "read_file" {
		t.Fatalf("tool=%v", tool)
	}
}

func TestBuildCodexRequestUsesNativeCodexToolsForShellAndApplyPatch(t *testing.T) {
	req := ChatRequest{
		Tools: []Tool{
			{Type: "function", Function: ToolFunctionSchema{Name: "Shell", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "ApplyPatch", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "ReadFile", Parameters: mustRaw(`{"type":"object"}`)}},
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: mustRaw(`"Please write your answer to a markdown file on disk"`),
		}},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	var sawShellCommand, sawApplyPatch, sawReadFile bool
	for _, raw := range out.Tools {
		tool, _ := raw.(map[string]any)
		switch {
		case tool["type"] == "function" && tool["name"] == "shell_command":
			sawShellCommand = true
		case tool["type"] == "custom" && tool["name"] == "apply_patch":
			sawApplyPatch = true
			format, _ := tool["format"].(map[string]any)
			if format["type"] != "grammar" || format["syntax"] != "lark" {
				t.Fatalf("apply_patch format=%v", format)
			}
			if !strings.Contains(format["definition"].(string), "begin_patch") {
				t.Fatalf("apply_patch grammar missing begin_patch")
			}
		case tool["type"] == "function" && tool["name"] == "read_file":
			sawReadFile = true
		case tool["name"] == "shell" || tool["name"] == "apply_patch":
			t.Fatalf("native tool was also emitted as generic function: %v", tool)
		}
	}
	if !sawShellCommand || !sawApplyPatch || !sawReadFile {
		t.Fatalf("native tools shell_command=%v apply_patch=%v read_file=%v tools=%v", sawShellCommand, sawApplyPatch, sawReadFile, out.Tools)
	}
}

func TestBuildCodexRequestPreservesCursorProductToolsForWriteIntent(t *testing.T) {
	req := ChatRequest{
		Tools: []Tool{
			{Type: "function", Function: ToolFunctionSchema{Name: "ReadFile", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "ApplyPatch", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "WebSearch", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "Subagent", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "SwitchMode", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "CreatePlan", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "CallMcpTool", Parameters: mustRaw(`{"type":"object"}`)}},
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: mustRaw(`"Please write your answer to a markdown file on disk"`),
		}},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	var names []string
	for _, raw := range out.Tools {
		tool, _ := raw.(map[string]any)
		if name, _ := tool["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"read_file", "apply_patch", "web_search", "spawn_agent", "switch_mode", "create_plan", "call_mcp_tool"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool names=%q missing %q", got, want)
		}
	}
}

func TestBuildCodexRequestStillPrunesUnknownToolsForWriteIntent(t *testing.T) {
	req := ChatRequest{
		Tools: []Tool{
			{Type: "function", Function: ToolFunctionSchema{Name: "ReadFile", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "UntrustedCustomTool", Parameters: mustRaw(`{"type":"object"}`)}},
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: mustRaw(`"Please write your answer to a markdown file on disk"`),
		}},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	var names []string
	for _, raw := range out.Tools {
		tool, _ := raw.(map[string]any)
		if name, _ := tool["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	got := strings.Join(names, ",")
	if got != "read_file" {
		t.Fatalf("tool names=%q", got)
	}
}

func TestBuildCodexRequestReplaysNativeShellAndApplyPatchHistory(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{
			{
				Role:    "assistant",
				Content: mustRaw(`""`),
				ToolCalls: []ToolCall{
					{
						Index: 0,
						ID:    "call_shell",
						Type:  "function",
						Function: ToolCallFunction{
							Name:      "Shell",
							Arguments: `{"command":"pwd","working_directory":"/repo","block_until_ms":1000}`,
						},
					},
					{
						Index: 1,
						ID:    "call_patch",
						Type:  "function",
						Function: ToolCallFunction{
							Name:      "ApplyPatch",
							Arguments: `{"input":"*** Begin Patch\n*** Add File: out.md\n+ok\n*** End Patch\n"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_shell", Content: mustRaw(`"ok"`)},
			{Role: "tool", ToolCallID: "call_patch", Content: mustRaw(`"Success"`)},
		},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	var sawShellCall, sawShellOutput, sawPatchCall, sawPatchOutput bool
	for _, item := range out.Input {
		switch codexItemTypeString(item) {
		case "function_call":
			if item["name"] != "shell_command" {
				continue
			}
			if !strings.Contains(item["arguments"].(string), `"command":"pwd"`) {
				t.Fatalf("shell command call=%v", item)
			}
			sawShellCall = true
		case "function_call_output":
			if item["call_id"] == "call_shell" {
				sawShellOutput = true
			}
		case "custom_tool_call":
			sawPatchCall = true
			if item["name"] != "apply_patch" || !strings.Contains(item["input"].(string), "*** Begin Patch") {
				t.Fatalf("patch call=%v", item)
			}
		case "custom_tool_call_output":
			if item["call_id"] == "call_patch" {
				sawPatchOutput = true
			}
		}
	}
	if !sawShellCall || !sawShellOutput || !sawPatchCall || !sawPatchOutput {
		t.Fatalf("shell_call=%v shell_output=%v patch_call=%v patch_output=%v input=%v", sawShellCall, sawShellOutput, sawPatchCall, sawPatchOutput, out.Input)
	}
}

func TestBuildCodexRequestSerializesAssistantToolCallsAndToolOutputs(t *testing.T) {
	req := ChatRequest{
		Messages: []ChatMessage{
			{
				Role:    "assistant",
				Content: mustRaw(`""`),
				ToolCalls: []ToolCall{{
					Index: 0,
					ID:    "call_1",
					Type:  "function",
					Function: ToolCallFunction{
						Name:      "ReadFile",
						Arguments: `{"path":"out.md"}`,
					},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Content:    mustRaw(`"ok"`),
			},
		},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4"}, "")
	var sawCall, sawOutput bool
	for _, item := range out.Input {
		switch codexItemTypeString(item) {
		case "function_call":
			sawCall = true
			if got, _ := item["name"].(string); got != "read_file" {
				t.Fatalf("function_call name=%q want read_file", got)
			}
		case "function_call_output":
			sawOutput = true
			if got, _ := item["call_id"].(string); got != "call_1" {
				t.Fatalf("call_id=%q want call_1", got)
			}
		}
	}
	if !sawCall {
		t.Fatalf("expected function_call in %#v", out.Input)
	}
	if !sawOutput {
		t.Fatalf("expected function_call_output in %#v", out.Input)
	}
}

func TestBuildCodexRequestPreservesResponsesInputToolHistory(t *testing.T) {
	req := ChatRequest{
		Input: mustRaw(`[
			{"role":"system","content":[{"type":"input_text","text":"system rules"}]},
			{"role":"user","content":[{"type":"input_text","text":"Please write your answer to a markdown file on disk"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"I will inspect markdown files."}]},
			{"type":"function_call","call_id":"call_glob","name":"Glob","arguments":"{\"glob_pattern\":\"*.md\",\"target_directory\":\"/repo\"}"},
			{"type":"function_call_output","call_id":"call_glob","output":[{"type":"input_text","text":"Result of search in '/repo' (total 1 files):\n- README.md\n"}]},
			{"role":"assistant","content":[{"type":"output_text","text":"I found one markdown file and will now write a new one."}]},
			{"type":"function_call","call_id":"call_shell","name":"Shell","arguments":"{\"command\":\"pwd\",\"working_directory\":\"/repo\",\"block_until_ms\":1000}"},
			{"type":"function_call_output","call_id":"call_shell","output":[{"type":"input_text","text":"Exit code: 0\n/repo\n"}]}
		]`),
		Tools: []Tool{
			{Type: "function", Function: ToolFunctionSchema{Name: "Shell", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "Glob", Parameters: mustRaw(`{"type":"object"}`)}},
			{Type: "function", Function: ToolFunctionSchema{Name: "ApplyPatch", Parameters: mustRaw(`{"type":"object"}`)}},
		},
	}

	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4"}, "")
	var sawDeveloper, sawGlobCall, sawGlobOutput, sawShellCommand, sawShellOutput bool
	for _, item := range out.Input {
		switch codexItemTypeString(item) {
		case "message":
			if item["role"] == "developer" && strings.Contains(codexInputContentText(item), "system rules") {
				sawDeveloper = true
			}
		case "function_call":
			switch item["call_id"] {
			case "call_glob":
				sawGlobCall = true
				if item["name"] != "glob" {
					t.Fatalf("glob call name=%v", item["name"])
				}
			case "call_shell":
				sawShellCommand = true
				if item["name"] != "shell_command" {
					t.Fatalf("shell call name=%v", item["name"])
				}
				if !strings.Contains(item["arguments"].(string), `"command":"pwd"`) {
					t.Fatalf("shell arguments=%v", item["arguments"])
				}
			}
		case "function_call_output":
			switch item["call_id"] {
			case "call_glob":
				sawGlobOutput = strings.Contains(item["output"].(string), "README.md")
			case "call_shell":
				sawShellOutput = strings.Contains(item["output"].(string), "/repo")
			}
		}
	}
	if !sawDeveloper || !sawGlobCall || !sawGlobOutput || !sawShellCommand || !sawShellOutput {
		t.Fatalf("developer=%v glob_call=%v glob_output=%v shell_call=%v shell_output=%v input=%v", sawDeveloper, sawGlobCall, sawGlobOutput, sawShellCommand, sawShellOutput, out.Input)
	}
}

func TestBuildCodexRequestAddsEncryptedReasoningIncludeAutomatically(t *testing.T) {
	req := ChatRequest{
		Reasoning: &adapteropenai.Reasoning{
			Effort: "medium",
		},
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, "")
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include=%v", out.Include)
	}
}

func TestBuildCodexRequestUsesStablePromptCacheKeyFromMetadata(t *testing.T) {
	req := ChatRequest{
		Metadata: mustRaw(`{"conversation_id":"thread-123"}`),
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, "")
	if out.PromptCache != "meta:thread-123" {
		t.Fatalf("prompt_cache_key=%q want %q", out.PromptCache, "meta:thread-123")
	}
}

func TestBuildCodexRequestFromCapturedWriteReplay(t *testing.T) {
	oldNow := NowFunc
	oldGetwd := GetwdFn
	oldShell := ShellNameFn
	NowFunc = func() time.Time { return time.Date(2026, 4, 25, 9, 0, 0, 0, time.FixedZone("PDT", -7*3600)) }
	GetwdFn = func() (string, error) { return "/Users/agoodkind/Sites/clyde-dev/clyde", nil }
	ShellNameFn = func() string { return "zsh" }
	defer func() {
		NowFunc = oldNow
		GetwdFn = oldGetwd
		ShellNameFn = oldShell
	}()

	raw, err := os.ReadFile("../testdata/codex_write_answer_request.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var req ChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	out := BuildRequest(req, ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4"}, "")
	if len(out.Tools) == 0 {
		t.Fatalf("expected tools")
	}
	if out.PromptCache == "" {
		t.Fatalf("expected prompt cache key")
	}
	foundPermissions := false
	foundEnvironment := false
	for _, item := range out.Input {
		text := codexInputContentText(item)
		if strings.Contains(text, "<permissions instructions>") {
			foundPermissions = true
		}
		if strings.Contains(text, "<environment_context>") {
			foundEnvironment = true
		}
	}
	if !foundPermissions || !foundEnvironment {
		t.Fatalf("missing contextual blocks permissions=%v environment=%v", foundPermissions, foundEnvironment)
	}
}

func TestBuildCodexRequestPrefersCursorConversationPromptCacheKey(t *testing.T) {
	req := ChatRequest{
		User:     "user-1",
		Metadata: mustRaw(`{"cursorConversationId":"conv-123"}`),
		Messages: []ChatMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}
	model := ResolvedModel{Alias: "gpt-5.4"}

	out := BuildRequest(req, model, "")
	if out.PromptCache != "cursor:conv-123" {
		t.Fatalf("prompt_cache_key=%q want %q", out.PromptCache, "cursor:conv-123")
	}
}

func TestCodexClientMetadataIncludesInstallationAndWindowIDs(t *testing.T) {
	got := ClientMetadata("acct-123", "cursor:conv-123:0")
	if got["x-codex-installation-id"] != "acct-123" {
		t.Fatalf("installation id=%q", got["x-codex-installation-id"])
	}
	if got["x-codex-window-id"] != "cursor:conv-123:0" {
		t.Fatalf("window id=%q", got["x-codex-window-id"])
	}
}

func TestBuildCodexManagedPromptPlanUsesAssistantAnchorForIncrementalPrompt(t *testing.T) {
	plan := buildManagedPromptPlanForTest([]ChatMessage{
		{Role: "system", Content: mustRaw(`"sys"`)},
		{Role: "user", Content: mustRaw(`"first user"`)},
		{Role: "assistant", Content: mustRaw(`"first answer"`)},
		{Role: "user", Content: mustRaw(`"second user"`)},
	})
	if plan.System != "sys" {
		t.Fatalf("System=%q", plan.System)
	}
	if !strings.Contains(plan.FullPrompt, "assistant: first answer") {
		t.Fatalf("FullPrompt=%q", plan.FullPrompt)
	}
	if plan.IncrementalPrompt != "user: second user" {
		t.Fatalf("IncrementalPrompt=%q", plan.IncrementalPrompt)
	}
	if plan.AssistantAnchor != "first answer" {
		t.Fatalf("AssistantAnchor=%q", plan.AssistantAnchor)
	}
}

func TestBuildCodexManagedPromptPlanStripsThinkingEnvelopeFromAssistantAnchor(t *testing.T) {
	assistant := mustRaw(`"<!--clyde-thinking-->\n> **💭 Thinking**\n> \n\n<!--/clyde-thinking-->\n\nFinal answer.\n"`)
	plan := buildManagedPromptPlanForTest([]ChatMessage{
		{Role: "user", Content: mustRaw(`"question"`)},
		{Role: "assistant", Content: assistant},
		{Role: "user", Content: mustRaw(`"follow up"`)},
	})
	if plan.AssistantAnchor != "Final answer." {
		t.Fatalf("AssistantAnchor=%q want %q", plan.AssistantAnchor, "Final answer.")
	}
}

func TestCodexLifecycleEventSummarizesFileChange(t *testing.T) {
	item := map[string]any{
		"type":   "fileChange",
		"status": "completed",
		"changes": []any{
			map[string]any{},
			map[string]any{},
		},
	}
	got, ok := LifecycleEvent(item, true)
	if !ok {
		t.Fatalf("expected lifecycle event")
	}
	if got.Kind != adapterrender.EventFileChangeCompleted {
		t.Fatalf("kind=%q", got.Kind)
	}
	if got.ChangeCount != 2 {
		t.Fatalf("change_count=%d", got.ChangeCount)
	}
}

func TestCodexPlanEventFormatsSteps(t *testing.T) {
	got, ok := PlanEvent("Clarifying tool usage", []map[string]string{
		{"step": "inspect payloads", "status": "completed"},
		{"step": "render tool output", "status": "inProgress"},
	})
	if !ok {
		t.Fatalf("expected plan event")
	}
	if got.PlanExplanation != "Clarifying tool usage" {
		t.Fatalf("explanation=%q", got.PlanExplanation)
	}
	if len(got.Plan) != 2 {
		t.Fatalf("plan_len=%d", len(got.Plan))
	}
}

func TestDeriveCodexCacheCreationTokens(t *testing.T) {
	if got := DeriveCacheCreationTokens(0, 4096); got != 4096 {
		t.Fatalf("DeriveCacheCreationTokens first turn=%d want 4096", got)
	}
	if got := DeriveCacheCreationTokens(4096, 6144); got != 2048 {
		t.Fatalf("DeriveCacheCreationTokens growth=%d want 2048", got)
	}
	if got := DeriveCacheCreationTokens(6144, 2048); got != 0 {
		t.Fatalf("DeriveCacheCreationTokens shrink=%d want 0", got)
	}
}

func TestParseCodexSSERetainsReasoningSignalWithoutVisibleText(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_text.delta",
		`data: {"delta":"Answer."}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":7}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	got, res, err := collectCodexSSEForTest(stream)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if !res.ReasoningSignaled {
		t.Fatalf("expected reasoning signal")
	}
	if res.ReasoningVisible {
		t.Fatalf("expected no visible reasoning text")
	}
	if got != "Answer." {
		t.Fatalf("streamed text = %q", got)
	}
}

func TestParseCodexSSEEmitsToolCallDeltas(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"item_id":"fc_1","delta":"{\"path\":\"out.md\"}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got []adapteropenai.StreamChunk
	res, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", res.FinishReason)
	}
	var deltas []adapteropenai.ToolCall
	for _, ch := range got {
		if len(ch.Choices) == 0 {
			continue
		}
		deltas = append(deltas, ch.Choices[0].Delta.ToolCalls...)
	}
	if len(deltas) < 2 {
		t.Fatalf("tool delta len=%d want >=2", len(deltas))
	}
	if deltas[0].Function.Name != "ReadFile" {
		t.Fatalf("first tool name=%q want ReadFile", deltas[0].Function.Name)
	}
	if deltas[1].Function.Arguments != `{"path":"out.md"}` {
		t.Fatalf("second args=%q", deltas[1].Function.Arguments)
	}
}

func TestParseCodexSSEEmitsToolArgumentsFromDoneWhenNoDeltaArrives(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		"",
		"event: response.output_item.done",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"out.md\"}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got []adapteropenai.StreamChunk
	res, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", res.FinishReason)
	}
	calls := collectToolCalls(got)
	if len(calls) != 2 {
		t.Fatalf("tool call chunks=%d want 2: %#v", len(calls), calls)
	}
	if calls[0].Function.Name != "ReadFile" {
		t.Fatalf("tool name=%q want ReadFile", calls[0].Function.Name)
	}
	if calls[1].Function.Arguments != `{"path":"out.md"}` {
		t.Fatalf("args=%q want full JSON", calls[1].Function.Arguments)
	}
}

func TestParseCodexSSEDoesNotDuplicateToolArgumentsOnDoneAfterDelta(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"write_file","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"item_id":"fc_1","delta":"{\"path\":\"out.md\"}"}`,
		"",
		"event: response.output_item.done",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"write_file","arguments":"{\"path\":\"out.md\"}"}}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got []adapteropenai.StreamChunk
	_, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	var args []string
	for _, ch := range got {
		if len(ch.Choices) == 0 {
			continue
		}
		for _, tc := range ch.Choices[0].Delta.ToolCalls {
			if tc.Function.Arguments != "" {
				args = append(args, tc.Function.Arguments)
			}
		}
	}
	if len(args) != 1 {
		t.Fatalf("argument delta count=%d want 1 (%v)", len(args), args)
	}
	if args[0] != `{"path":"out.md"}` {
		t.Fatalf("args=%q want single full JSON", args[0])
	}
}

func TestParseCodexSSEMapsToolAliasesBackToCursorNames(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"item_id":"fc_1","delta":"{\"path\":\"out.md\"}"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}},"sequence_number":10}`,
		"",
	}, "\n") + "\n")
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got []adapteropenai.StreamChunk
	_, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	var names []string
	for _, ch := range got {
		if len(ch.Choices) == 0 {
			continue
		}
		for _, tc := range ch.Choices[0].Delta.ToolCalls {
			if tc.Function.Name != "" {
				names = append(names, tc.Function.Name)
			}
		}
	}
	if len(names) != 1 || names[0] != "ReadFile" {
		t.Fatalf("tool names=%v want [ReadFile]", names)
	}
}

func TestParseCodexSSEMapsNativeLocalShellToCursorShell(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.done",
		`data: {"item":{"id":"ls_1","type":"local_shell_call","call_id":"call_shell","status":"completed","action":{"type":"exec","command":["zsh","-lc","pwd"],"working_directory":"/repo","timeout_ms":1000}}}`,
		"",
		"event: response.completed",
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n") + "\n")
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got []adapteropenai.StreamChunk
	res, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q", res.FinishReason)
	}
	calls := collectToolCalls(got)
	if len(calls) != 2 {
		t.Fatalf("tool call chunks=%d want 2: %#v", len(calls), calls)
	}
	if calls[0].Function.Name != "Shell" {
		t.Fatalf("tool name=%q want Shell", calls[0].Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[1].Function.Arguments), &args); err != nil {
		t.Fatalf("args JSON: %v", err)
	}
	if args["command"] != "pwd" || args["working_directory"] != "/repo" || args["block_until_ms"].(float64) != 1000 {
		t.Fatalf("args=%v", args)
	}
}

func TestParseCodexSSEMapsShellCommandToCursorShell(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_shell","name":"shell_command","arguments":""}}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"item_id":"fc_1","delta":"{\"command\":\"pwd\","}`,
		"",
		"event: response.function_call_arguments.delta",
		`data: {"item_id":"fc_1","delta":"\"workdir\":\"/repo\",\"timeout_ms\":1000}"}`,
		"",
		"event: response.output_item.done",
		`data: {"item":{"id":"fc_1","type":"function_call","call_id":"call_shell","name":"shell_command","arguments":""}}`,
		"",
		"event: response.completed",
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n") + "\n")
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got []adapteropenai.StreamChunk
	res, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q", res.FinishReason)
	}
	calls := collectToolCalls(got)
	if len(calls) != 2 {
		t.Fatalf("tool call chunks=%d want 2: %#v", len(calls), calls)
	}
	if calls[0].Function.Name != "Shell" {
		t.Fatalf("tool name=%q want Shell", calls[0].Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[1].Function.Arguments), &args); err != nil {
		t.Fatalf("args JSON: %v", err)
	}
	if args["command"] != "pwd" || args["working_directory"] != "/repo" || args["block_until_ms"].(float64) != 1000 {
		t.Fatalf("args=%v", args)
	}
}

func TestParseCodexSSEMapsNativeApplyPatchToCursorApplyPatch(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: out.md\n+ok\n*** End Patch\n"
	stream := strings.NewReader(strings.Join([]string{
		"event: response.output_item.added",
		`data: {"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":""}}`,
		"",
		"event: response.custom_tool_call_input.delta",
		`data: {"item_id":"ct_1","call_id":"call_patch","delta":"*** Begin Patch\n"}`,
		"",
		"event: response.custom_tool_call_input.delta",
		`data: {"item_id":"ct_1","call_id":"call_patch","delta":"*** Add File: out.md\n+ok\n*** End Patch\n"}`,
		"",
		"event: response.output_item.done",
		`data: {"item":{"id":"ct_1","type":"custom_tool_call","call_id":"call_patch","name":"apply_patch","input":""}}`,
		"",
		"event: response.completed",
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`,
		"",
	}, "\n") + "\n")
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got []adapteropenai.StreamChunk
	res, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		got = append(got, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q", res.FinishReason)
	}
	calls := collectToolCalls(got)
	if len(calls) != 3 {
		t.Fatalf("tool call chunks=%d want 3: %#v", len(calls), calls)
	}
	if calls[0].Function.Name != "ApplyPatch" {
		t.Fatalf("tool name=%q want ApplyPatch", calls[0].Function.Name)
	}
	if gotPatch := calls[1].Function.Arguments + calls[2].Function.Arguments; gotPatch != patch {
		t.Fatalf("patch args=%q want %q", gotPatch, patch)
	}
}

func TestParseCodexSSESeparatesSummaryFromReasoningBody(t *testing.T) {
	stream := strings.NewReader(strings.Join([]string{
		"event: response.reasoning_summary_text.delta",
		`data: {"delta":"Exploring pet-color constraints"}`,
		"",
		"event: response.reasoning_text.delta",
		`data: {"delta":"I am checking combinations."}`,
		"",
		"event: response.output_text.delta",
		`data: {"delta":"Final answer."}`,
		"",
		"event: response.completed",
		`data: {"response":{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":7}}}}`,
		"",
	}, "\n") + "\n")
	got, _, err := collectCodexSSEForTest(stream)
	if err != nil {
		t.Fatalf("ParseSSE: %v", err)
	}
	out := got
	if !strings.Contains(out, "Exploring pet-color constraints\n> \n> I am checking combinations.") {
		t.Fatalf("expected blank-line-separated reasoning sections, got %q", out)
	}
	if !strings.Contains(out, "Final answer.") {
		t.Fatalf("missing final answer: %q", out)
	}
}

func TestCodexRendererSeparatesSummarySections(t *testing.T) {
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	firstChunks := r.HandleEvent(adapterrender.Event{Kind: adapterrender.EventReasoningDelta, Text: "First heading", ReasoningKind: "summary", SummaryIndex: codexIntPtr(0)})
	secondChunks := r.HandleEvent(adapterrender.Event{Kind: adapterrender.EventReasoningDelta, Text: "Second heading", ReasoningKind: "summary", SummaryIndex: codexIntPtr(1)})
	first := firstChunks[0].Choices[0].Delta.Content
	second := secondChunks[0].Choices[0].Delta.Content
	if !strings.Contains(first, "<!--clyde-thinking-->") {
		t.Fatalf("missing opening envelope: %q", first)
	}
	if !strings.Contains(second, "\n> \n> Second heading") {
		t.Fatalf("expected summary separation, got %q", second)
	}
}

func TestCodexRendererSeparatesBoldSummaryHeadingWithoutIndexChange(t *testing.T) {
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	_ = r.HandleEvent(adapterrender.Event{Kind: adapterrender.EventReasoningDelta, Text: "First paragraph.", ReasoningKind: "summary"})
	secondChunks := r.HandleEvent(adapterrender.Event{Kind: adapterrender.EventReasoningDelta, Text: "**Second heading**", ReasoningKind: "summary"})
	second := secondChunks[0].Choices[0].Delta.Content
	if !strings.Contains(second, "\n> \n> **Second heading**") {
		t.Fatalf("expected bold heading separation, got %q", second)
	}
}

func TestCodexRendererEmitsSyntheticThinkingPlaceholderWhenSignaledWithoutVisibleText(t *testing.T) {
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	_ = r.HandleEvent(adapterrender.Event{Kind: adapterrender.EventReasoningSignaled})
	chunks := r.HandleEvent(adapterrender.Event{Kind: adapterrender.EventReasoningFinished})
	if len(chunks) != 1 {
		t.Fatalf("chunks=%d want 1", len(chunks))
	}
	got := chunks[0].Choices[0].Delta.Content
	if !strings.Contains(got, "<!--clyde-thinking-->") || !strings.Contains(got, "<!--/clyde-thinking-->") {
		t.Fatalf("missing synthetic thinking envelope: %q", got)
	}
}

func collectCodexSSEForTest(stream *strings.Reader) (string, RunResult, error) {
	r := adapterrender.NewEventRenderer("req", "alias", "codex", nil)
	var got strings.Builder
	res, err := ParseSSE(stream, r, func(ch adapteropenai.StreamChunk) error {
		if len(ch.Choices) > 0 {
			got.WriteString(ch.Choices[0].Delta.Content)
		}
		return nil
	})
	return got.String(), res, err
}

func collectToolCalls(chunks []adapteropenai.StreamChunk) []adapteropenai.ToolCall {
	var out []adapteropenai.ToolCall
	for _, ch := range chunks {
		if len(ch.Choices) == 0 {
			continue
		}
		out = append(out, ch.Choices[0].Delta.ToolCalls...)
	}
	return out
}

func codexIntPtr(v int) *int {
	return &v
}

func codexInputContentType(item codexInputItem) string {
	content, _ := item["content"].([]map[string]any)
	if len(content) == 0 {
		return ""
	}
	v, _ := content[0]["type"].(string)
	return v
}

func codexInputContentText(item codexInputItem) string {
	content, _ := item["content"].([]map[string]any)
	if len(content) == 0 {
		return ""
	}
	v, _ := content[0]["text"].(string)
	return v
}

func codexItemTypeString(item codexInputItem) string {
	v, _ := item["type"].(string)
	return v
}

func TestBuildCodexRequestParityMatrixPreservesAliasIntent(t *testing.T) {
	cases := []struct {
		name                 string
		model                ResolvedModel
		metadata             []byte
		maxCompletion        *int
		maxOutput            *int
		serviceTier          string
		text                 []byte
		truncation           string
		promptCacheRetention string
		wantModel            string
		wantTier             string
		wantMax              int
	}{
		{
			name:      "native_alias_preserves_upstream_model",
			model:     ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4"},
			wantModel: "gpt-5.4",
		},
		{
			name:      "native_long_context_alias_preserves_upstream_model",
			model:     ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4"},
			wantModel: "gpt-5.4",
		},
		{
			name:      "spark_alias_preserves_spark_slug",
			model:     ResolvedModel{Alias: "gpt-5.3-codex-spark", ClaudeModel: "gpt-5.3-codex-spark"},
			wantModel: "gpt-5.3-codex-spark",
		},
		{
			name:      "service_tier_and_max_completion_passthrough",
			model:     ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4"},
			metadata:  mustRaw(`{"service_tier":"fast"}`),
			wantModel: "gpt-5.4",
			wantTier:  "priority",
			wantMax:   4096,
			maxCompletion: func() *int {
				v := 4096
				return &v
			}(),
		},
		{
			name:      "responses_controls_passthrough",
			model:     ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4"},
			wantModel: "gpt-5.4",
			wantTier:  "priority",
			wantMax:   8192,
			maxOutput: func() *int {
				v := 8192
				return &v
			}(),
			serviceTier:          "fast",
			text:                 mustRaw(`{"verbosity":"high"}`),
			truncation:           "auto",
			promptCacheRetention: "24h",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ChatRequest{
				Metadata:             tc.metadata,
				MaxComplTokens:       tc.maxCompletion,
				MaxOutputTokens:      tc.maxOutput,
				ServiceTier:          tc.serviceTier,
				Text:                 tc.text,
				Truncation:           tc.truncation,
				PromptCacheRetention: tc.promptCacheRetention,
				Messages: []ChatMessage{{
					Role:    "user",
					Content: json.RawMessage(`"hello"`),
				}},
			}
			out := BuildRequest(req, tc.model, "")
			if out.Model != tc.wantModel {
				t.Fatalf("model=%q want %q", out.Model, tc.wantModel)
			}
			if tc.wantTier != "" && out.ServiceTier != tc.wantTier {
				t.Fatalf("service_tier=%q want %q", out.ServiceTier, tc.wantTier)
			}
			if tc.wantMax != 0 {
				if out.MaxCompletion == nil || *out.MaxCompletion != tc.wantMax {
					t.Fatalf("max_completion_tokens=%v want %d", out.MaxCompletion, tc.wantMax)
				}
			}
			if len(tc.text) > 0 && string(out.Text) != string(tc.text) {
				t.Fatalf("text=%s want %s", out.Text, tc.text)
			}
			if tc.truncation != "" && out.Truncation != tc.truncation {
				t.Fatalf("truncation=%q want %q", out.Truncation, tc.truncation)
			}
			if tc.promptCacheRetention != "" && out.PromptCacheRetention != tc.promptCacheRetention {
				t.Fatalf("prompt_cache_retention=%q want %q", out.PromptCacheRetention, tc.promptCacheRetention)
			}
		})
	}
}
