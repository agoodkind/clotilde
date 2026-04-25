package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type codexInputContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type codexInputItem map[string]any

type codexRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include,omitempty"`
	PromptCache       string            `json:"prompt_cache_key,omitempty"`
	ClientMetadata    map[string]string `json:"client_metadata,omitempty"`
	Reasoning         *codexReasoning   `json:"reasoning,omitempty"`
	Input             []codexInputItem  `json:"input"`
	Tools             []any             `json:"tools,omitempty"`
	ToolChoice        string            `json:"tool_choice,omitempty"`
	ParallelToolCalls bool              `json:"parallel_tool_calls,omitempty"`
}

type codexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type codexCompleted struct {
	Response struct {
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

type codexRunResult struct {
	Usage                      Usage
	FinishReason               string
	ReasoningSignaled          bool
	ReasoningVisible           bool
	DerivedCacheCreationTokens int
}

type codexReasoningStreamState struct {
	LastKind       string
	LastSummaryIdx int
	HaveSummaryIdx bool
	PendingBreak   bool
}

type codexToolCallState struct {
	Index             int
	ItemID            string
	CallID            string
	Name              string
	NativeName        string
	Type              string
	ArgumentDeltaSeen bool
	ArgumentsEmitted  bool
	Arguments         strings.Builder
	Input             strings.Builder
}

var (
	codexNow       = time.Now
	codexGetwd     = os.Getwd
	codexShellName = func() string {
		shell := strings.TrimSpace(os.Getenv("SHELL"))
		if shell == "" {
			return "sh"
		}
		parts := strings.Split(shell, "/")
		return parts[len(parts)-1]
	}
)

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
}

var codexToolNameReverseAliases = func() map[string]string {
	out := make(map[string]string, len(codexToolNameAliases))
	for orig, alias := range codexToolNameAliases {
		out[alias] = orig
	}
	return out
}()

func codexOutboundToolName(name string) string {
	name = strings.TrimSpace(name)
	if alias := codexToolNameAliases[name]; alias != "" {
		return alias
	}
	return name
}

func codexInboundToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "shell_command" {
		return "Shell"
	}
	if orig := codexToolNameReverseAliases[name]; orig != "" {
		return orig
	}
	return name
}

func sanitizeForUpstreamCache(text string) string {
	text = tooltrans.StripNoticeSentinel(text)
	text = tooltrans.StripActivitySentinel(text)
	text = tooltrans.StripThinkingSentinel(text)
	return text
}

func mapCodexUsage(c codexCompleted) Usage {
	u := Usage{
		PromptTokens:     c.Response.Usage.InputTokens,
		CompletionTokens: c.Response.Usage.OutputTokens,
		TotalTokens:      c.Response.Usage.TotalTokens,
	}
	if ct := c.Response.Usage.InputTokensDetails.CachedTokens; ct > 0 {
		u.PromptTokensDetails = &PromptTokensDetails{CachedTokens: ct}
	}
	return u
}

func codexReasoningTokens(raw map[string]any) int {
	response, _ := raw["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	details, _ := usage["output_tokens_details"].(map[string]any)
	switch v := details["reasoning_tokens"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func codexMapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func codexRawString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

func codexMapSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	v, _ := m[key].([]any)
	return v
}

func codexItemType(item map[string]any) string {
	return codexMapString(item, "type")
}

func codexItemStatus(item map[string]any) string {
	return codexMapString(item, "status")
}

func codexFileChangeCount(item map[string]any) int {
	changes := codexMapSlice(item, "changes")
	count := len(changes)
	if count == 0 {
		count = 1
	}
	return count
}

func codexToolName(item map[string]any) string {
	if cmd := codexMapString(item, "command"); cmd != "" {
		return cmd
	}
	if tool := codexMapString(item, "tool"); tool != "" {
		return tool
	}
	server := codexMapString(item, "server")
	tool := codexMapString(item, "tool")
	name := strings.Trim(strings.Join([]string{server, tool}, "/"), "/")
	if name != "" {
		return name
	}
	if typ := codexItemType(item); typ != "" {
		return typ
	}
	return "tool"
}

func codexPlanEvent(explanation string, plan []map[string]string) (tooltrans.Event, bool) {
	ev := tooltrans.Event{
		Kind:            tooltrans.EventPlanUpdated,
		PlanExplanation: strings.TrimSpace(explanation),
		Plan:            make([]tooltrans.EventPlanStep, 0, len(plan)),
	}
	for _, step := range plan {
		label := strings.TrimSpace(step["step"])
		if label == "" {
			continue
		}
		ev.Plan = append(ev.Plan, tooltrans.EventPlanStep{
			Step:   label,
			Status: strings.TrimSpace(step["status"]),
		})
	}
	if ev.PlanExplanation == "" && len(ev.Plan) == 0 {
		return tooltrans.Event{}, false
	}
	return ev, true
}

func codexLifecycleEvent(item map[string]any, completed bool) (tooltrans.Event, bool) {
	itemType := codexItemType(item)
	status := codexItemStatus(item)
	switch itemType {
	case "commandExecution", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "contextCompaction":
		kind := tooltrans.EventToolStarted
		if completed {
			kind = tooltrans.EventToolCompleted
		}
		return tooltrans.Event{
			Kind:       kind,
			ItemType:   itemType,
			ItemStatus: status,
			ItemID:     codexMapString(item, "id"),
			ToolName:   codexToolName(item),
			ServerName: codexMapString(item, "server"),
			Command:    codexMapString(item, "command"),
			Completed:  completed,
		}, true
	case "fileChange":
		kind := tooltrans.EventFileChangeStarted
		if completed {
			kind = tooltrans.EventFileChangeCompleted
		}
		return tooltrans.Event{
			Kind:        kind,
			ItemType:    itemType,
			ItemStatus:  status,
			ItemID:      codexMapString(item, "id"),
			ChangeCount: codexFileChangeCount(item),
			Completed:   completed,
		}, true
	default:
		return tooltrans.Event{}, false
	}
}

func codexProgressEvent(method, itemID, text string) (tooltrans.Event, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return tooltrans.Event{}, false
	}
	switch method {
	case "item/fileChange/outputDelta", "item/fileChange/patchUpdated":
		return tooltrans.Event{
			Kind:     tooltrans.EventFileChangeProgress,
			ItemID:   itemID,
			ItemType: "fileChange",
			Text:     text,
		}, true
	default:
		return tooltrans.Event{
			Kind:   tooltrans.EventToolProgress,
			ItemID: itemID,
			Text:   text,
		}, true
	}
}

func logCodexTransportEvent(
	log *slog.Logger,
	ctx context.Context,
	requestID string,
	msg string,
	attrs ...slog.Attr,
) {
	if log == nil {
		log = slog.Default()
	}
	base := []slog.Attr{
		slog.String("component", "adapter"),
		slog.String("subcomponent", "codex"),
		slog.String("request_id", requestID),
	}
	base = append(base, attrs...)
	log.LogAttrs(ctx, slog.LevelDebug, msg, base...)
}

func logCodexReasoningEvent(
	log *slog.Logger,
	ctx context.Context,
	requestID string,
	event string,
	attrs ...slog.Attr,
) {
	attrs = append([]slog.Attr{slog.String("event", event)}, attrs...)
	logCodexTransportEvent(log, ctx, requestID, "adapter.codex.reasoning.event", attrs...)
}

func logCodexToolingEvent(
	log *slog.Logger,
	ctx context.Context,
	requestID string,
	event string,
	attrs ...slog.Attr,
) {
	attrs = append([]slog.Attr{slog.String("event", event)}, attrs...)
	logCodexTransportEvent(log, ctx, requestID, "adapter.codex.tooling.event", attrs...)
}

func logAdapterProtocolEvent(ctx context.Context, requestID, backend, event string, attrs ...slog.Attr) {
	base := []slog.Attr{
		slog.String("component", "adapter"),
		slog.String("subcomponent", backend),
		slog.String("request_id", requestID),
		slog.String("backend", backend),
		slog.String("event", event),
	}
	base = append(base, attrs...)
	slog.LogAttrs(ctx, slog.LevelDebug, "adapter.protocol.event", base...)
}

func emitCodexRendered(
	renderer *tooltrans.EventRenderer,
	ev tooltrans.Event,
	emit func(tooltrans.OpenAIStreamChunk) error,
	assistantText *strings.Builder,
) error {
	for _, ch := range renderer.HandleEvent(ev) {
		if assistantText != nil && len(ch.Choices) > 0 {
			assistantText.WriteString(ch.Choices[0].Delta.Content)
		}
		if err := emit(ch); err != nil {
			return err
		}
	}
	return nil
}

func codexParallelToolCalls(req ChatRequest) bool {
	if req.ParallelTools == nil {
		return true
	}
	return *req.ParallelTools
}

func codexRequestTools(req ChatRequest) []Tool {
	var tools []Tool
	if len(req.Tools) > 0 {
		tools = append(tools, req.Tools...)
	} else if len(req.Functions) > 0 {
		for _, fn := range req.Functions {
			tools = append(tools, Tool{
				Type: "function",
				Function: ToolFunctionSchema{
					Name:        fn.Name,
					Description: fn.Description,
					Parameters:  fn.Parameters,
				},
			})
		}
	}
	if !codexHasWriteIntent(req) {
		return tools
	}
	allowed := map[string]bool{
		"Shell":        true,
		"AwaitShell":   true,
		"Glob":         true,
		"rg":           true,
		"ReadFile":     true,
		"Delete":       true,
		"ApplyPatch":   true,
		"EditNotebook": true,
		"ReadLints":    true,
	}
	filtered := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if allowed[strings.TrimSpace(tool.Function.Name)] {
			filtered = append(filtered, tool)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return tools
}

func codexToolSpecs(req ChatRequest, modelName string) []any {
	tools := codexRequestTools(req)
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	emittedNativeShell := false
	emittedNativeApplyPatch := false
	shellMode := codexShellToolMode(modelName)
	for _, tool := range tools {
		toolName := strings.TrimSpace(tool.Function.Name)
		if codexIsShellToolName(toolName) {
			if !emittedNativeShell {
				switch shellMode {
				case "local_shell":
					out = append(out, codexNativeLocalShellSpec())
				case "shell_command":
					out = append(out, codexShellCommandSpec())
				default:
					out = append(out, codexFunctionToolSpec("shell", tool.Function.Description, tool.Function.Parameters, tool.Function.Strict))
				}
				emittedNativeShell = true
			}
			continue
		}
		if codexIsApplyPatchToolName(toolName) {
			if !emittedNativeApplyPatch {
				out = append(out, codexNativeApplyPatchSpec())
				emittedNativeApplyPatch = true
			}
			continue
		}
		out = append(out, codexFunctionToolSpec(codexOutboundToolName(toolName), tool.Function.Description, tool.Function.Parameters, tool.Function.Strict))
	}
	return out
}

func codexMessageContent(role, textType, text string) codexInputItem {
	return codexInputItem{
		"type": "message",
		"role": role,
		"content": []codexInputContent{{
			Type: textType,
			Text: text,
		}},
	}
}

func codexBaseInstructions(modelName string) string {
	if text := adaptercodex.BaseInstructions(modelName); text != "" {
		return text
	}
	return "You are a helpful assistant."
}

func codexPermissionsText() string {
	return strings.TrimSpace(`
<permissions instructions>
Filesystem sandboxing defines which files can be read or written. ` + "`sandbox_mode`" + ` is ` + "`danger-full-access`" + `: No filesystem sandboxing - all commands are permitted. Network access is enabled.
Approval policy is currently ` + "`never`" + `. Use the available tools directly when they help complete the task.
</permissions instructions>`)
}

func codexToolUseContractText() string {
	return strings.TrimSpace(`
<tool_calling_instructions>
- When you call a tool, emit complete JSON arguments that satisfy the tool schema.
- Never emit empty tool arguments like {}` + "`" + ` or omit required fields.
- Use the cwd from ` + "`<environment_context>`" + ` to derive sensible absolute paths when a tool schema expects them.
- For file write or edit requests, prefer taking the next reasonable tool action over asking for clarification when the workspace context is sufficient.
- If you need to inspect the workspace before writing, call the appropriate search/read tools with concrete arguments.
</tool_calling_instructions>`)
}

func codexPermissionsMessage() codexInputItem {
	return codexMessageContent("developer", "input_text", codexPermissionsText())
}

func codexRequestWorkspacePath(req ChatRequest) string {
	return adaptercursor.WorkspacePath(req)
}

func codexEnvironmentContextText(workspacePath string) (string, bool) {
	cwd := strings.TrimSpace(workspacePath)
	if cwd == "" {
		var err error
		cwd, err = codexGetwd()
		if err != nil {
			return "", false
		}
	}
	now := codexNow()
	return strings.TrimSpace(fmt.Sprintf(`
<environment_context>
  <cwd>%s</cwd>
  <shell>%s</shell>
  <current_date>%s</current_date>
  <timezone>%s</timezone>
</environment_context>`,
		cwd,
		codexShellName(),
		now.Format("2006-01-02"),
		now.Format("-07:00 MST"),
	)), true
}

func codexEnvironmentContextMessage(workspacePath string) (codexInputItem, bool) {
	text, ok := codexEnvironmentContextText(workspacePath)
	if !ok {
		return nil, false
	}
	return codexMessageContent("user", "input_text", text), true
}

func codexFunctionCallItem(tc ToolCall, shellMode string) codexInputItem {
	if codexIsShellToolName(codexToolCallName(tc)) {
		if shellMode == "shell_command" {
			return codexShellCommandCallItem(tc)
		}
		return codexLocalShellCallItem(tc)
	}
	if codexIsApplyPatchToolName(codexToolCallName(tc)) {
		return codexApplyPatchCallItem(tc)
	}
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = fmt.Sprintf("call_%d", tc.Index)
	}
	return codexInputItem{
		"type":      "function_call",
		"call_id":   callID,
		"name":      codexOutboundToolName(tc.Function.Name),
		"arguments": tc.Function.Arguments,
	}
}

func codexFunctionCallOutputItem(callID, text string) codexInputItem {
	return codexInputItem{
		"type":    "function_call_output",
		"call_id": strings.TrimSpace(callID),
		"output":  text,
	}
}

func codexResponsesContentText(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return sanitizeForUpstreamCache(v)
	case []any:
		var parts []string
		for _, part := range v {
			m, _ := part.(map[string]any)
			if m == nil {
				continue
			}
			switch strings.TrimSpace(codexMapString(m, "type")) {
			case "text", "input_text", "output_text":
				if text := codexRawString(m, "text"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return sanitizeForUpstreamCache(strings.Join(parts, "\n"))
	case map[string]any:
		switch strings.TrimSpace(codexMapString(v, "type")) {
		case "text", "input_text", "output_text":
			return sanitizeForUpstreamCache(codexRawString(v, "text"))
		}
	}
	return ""
}

func codexResponsesOutputText(raw any) string {
	text := codexResponsesContentText(raw)
	if text != "" {
		return text
	}
	switch v := raw.(type) {
	case string:
		return sanitizeForUpstreamCache(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return sanitizeForUpstreamCache(string(b))
	}
}

func codexRewriteWorkspacePath(text, workspacePath string) string {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" || text == "" {
		return text
	}
	cwd, err := codexGetwd()
	if err != nil {
		return text
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || cwd == workspacePath {
		return text
	}
	return strings.ReplaceAll(text, cwd, workspacePath)
}

func codexFunctionCallFromResponsesItem(item map[string]any, modelName, workspacePath string) (codexInputItem, string) {
	callID := codexMapString(item, "call_id")
	name := codexMapString(item, "name")
	args := codexRewriteWorkspacePath(codexRawString(item, "arguments"), workspacePath)
	tc := ToolCall{
		ID:   callID,
		Type: "function",
		Function: ToolCallFunction{
			Name:      codexInboundToolName(name),
			Arguments: args,
		},
	}
	return codexFunctionCallItem(tc, codexShellToolMode(modelName)), tc.Function.Name
}

func codexInputFromResponsesInput(
	raw json.RawMessage,
	modelName string,
	workspacePath string,
	developerSections *[]string,
	buildContextual func() []codexInputItem,
) ([]codexInputItem, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, false
	}
	lastUserIdx := -1
	for i, item := range items {
		if strings.EqualFold(codexMapString(item, "role"), "user") {
			lastUserIdx = i
		}
	}
	input := make([]codexInputItem, 0, len(items)+2)
	toolCallNames := make(map[string]string)
	insertedContext := false
	for idx, item := range items {
		role := strings.ToLower(codexMapString(item, "role"))
		itemType := strings.TrimSpace(codexMapString(item, "type"))
		switch {
		case role == "system" || role == "developer":
			if text := strings.TrimSpace(codexResponsesContentText(item["content"])); text != "" {
				*developerSections = append(*developerSections, text)
			}
		case role == "user":
			if !insertedContext && idx == lastUserIdx {
				if contextual := buildContextual(); len(contextual) > 0 {
					input = append(input, contextual...)
				}
				insertedContext = true
			}
			if text := strings.TrimSpace(codexResponsesContentText(item["content"])); text != "" {
				input = append(input, codexMessageContent("user", "input_text", text))
			}
		case role == "assistant":
			if text := strings.TrimSpace(codexResponsesContentText(item["content"])); text != "" {
				input = append(input, codexMessageContent("assistant", "output_text", text))
			}
		case itemType == "function_call":
			call, toolName := codexFunctionCallFromResponsesItem(item, modelName, workspacePath)
			if callID := codexMapString(item, "call_id"); callID != "" {
				toolCallNames[callID] = toolName
			}
			input = append(input, call)
		case itemType == "function_call_output":
			callID := codexMapString(item, "call_id")
			output := strings.TrimSpace(codexRewriteWorkspacePath(codexResponsesOutputText(item["output"]), workspacePath))
			if output == "" {
				continue
			}
			if codexIsApplyPatchToolName(toolCallNames[callID]) {
				input = append(input, codexCustomToolCallOutputItem(callID, output))
			} else {
				input = append(input, codexFunctionCallOutputItem(callID, output))
			}
		case itemType == "custom_tool_call":
			callID := codexMapString(item, "call_id")
			name := codexMapString(item, "name")
			inputText := codexRewriteWorkspacePath(codexUnwrapApplyPatchInput(codexRawString(item, "input")), workspacePath)
			if callID != "" {
				toolCallNames[callID] = codexInboundToolName(name)
			}
			input = append(input, codexInputItem{
				"type":    "custom_tool_call",
				"call_id": callID,
				"name":    name,
				"input":   inputText,
			})
		case itemType == "custom_tool_call_output":
			callID := codexMapString(item, "call_id")
			output := strings.TrimSpace(codexRewriteWorkspacePath(codexResponsesOutputText(item["output"]), workspacePath))
			if output != "" {
				input = append(input, codexCustomToolCallOutputItem(callID, output))
			}
		}
	}
	if !insertedContext {
		if contextual := buildContextual(); len(contextual) > 0 {
			input = append(input, contextual...)
		}
	}
	return input, len(input) > 0
}

func buildCodexRequest(req ChatRequest, model ResolvedModel, effort string) codexRequest {
	input := make([]codexInputItem, 0, len(req.Messages))
	developerSections := make([]string, 0, 8)
	contextualUserSections := make([]string, 0, 4)
	toolCallNames := make(map[string]string)
	modelName := strings.TrimSpace(model.ClaudeModel)
	if modelName == "" {
		modelName = model.Alias
	}
	lastUserIdx := -1
	for i, m := range req.Messages {
		if strings.EqualFold(m.Role, "user") {
			lastUserIdx = i
		}
	}
	workspacePath := codexRequestWorkspacePath(req)
	developerSections = append(developerSections, codexPermissionsText())
	developerSections = append(developerSections, codexToolUseContractText())
	if env, ok := codexEnvironmentContextText(workspacePath); ok {
		contextualUserSections = append(contextualUserSections, env)
	}
	buildContextual := func() []codexInputItem {
		contextual := make([]codexInputItem, 0, 2)
		if len(developerSections) > 0 {
			contextual = append(contextual, codexMessageContent("developer", "input_text", strings.Join(developerSections, "\n\n")))
		}
		if len(contextualUserSections) > 0 {
			contextual = append(contextual, codexMessageContent("user", "input_text", strings.Join(contextualUserSections, "\n\n")))
		}
		return contextual
	}
	if rawInput, ok := codexInputFromResponsesInput(req.Input, modelName, workspacePath, &developerSections, buildContextual); ok {
		input = rawInput
	} else {
		insertedContext := false
		for idx, m := range req.Messages {
			text := sanitizeForUpstreamCache(FlattenContent(m.Content))
			text = strings.TrimSpace(text)
			switch strings.ToLower(m.Role) {
			case "system", "developer":
				if text != "" {
					developerSections = append(developerSections, text)
				}
				continue
			case "assistant":
				for _, tc := range m.ToolCalls {
					if strings.TrimSpace(tc.Function.Name) == "" {
						continue
					}
					callID := strings.TrimSpace(tc.ID)
					if callID == "" {
						callID = fmt.Sprintf("call_%d", tc.Index)
					}
					toolCallNames[callID] = codexToolCallName(tc)
					input = append(input, codexFunctionCallItem(tc, codexShellToolMode(modelName)))
				}
				if text != "" {
					input = append(input, codexMessageContent("assistant", "output_text", text))
				}
			case "tool", "function":
				if text != "" && strings.TrimSpace(m.ToolCallID) != "" {
					if codexIsApplyPatchToolName(toolCallNames[strings.TrimSpace(m.ToolCallID)]) {
						input = append(input, codexCustomToolCallOutputItem(m.ToolCallID, text))
					} else {
						input = append(input, codexFunctionCallOutputItem(m.ToolCallID, text))
					}
				} else if text != "" {
					input = append(input, codexMessageContent("user", "input_text", "tool: "+text))
				}
			default:
				if !insertedContext && idx == lastUserIdx {
					if contextual := buildContextual(); len(contextual) > 0 {
						input = append(input, contextual...)
					}
					insertedContext = true
				}
				if text != "" {
					input = append(input, codexMessageContent("user", "input_text", text))
				}
			}
		}
		if !insertedContext {
			if contextual := buildContextual(); len(contextual) > 0 {
				input = append(input, contextual...)
			}
		}
	}
	instructions := codexBaseInstructions(modelName)
	if len(input) == 0 {
		input = append(input, codexMessageContent("user", "input_text", " "))
	}
	reasoning := effectiveCodexReasoning(req, effort)
	include := codexRequestInclude(req.Include, reasoning != nil)
	promptCacheKey := requestContextTrackerKey(req, model.Alias)
	return codexRequest{
		Model:             modelName,
		Instructions:      instructions,
		Store:             false,
		Stream:            true,
		Include:           include,
		PromptCache:       promptCacheKey,
		Reasoning:         reasoning,
		Input:             input,
		Tools:             codexToolSpecs(req, modelName),
		ToolChoice:        "auto",
		ParallelToolCalls: codexParallelToolCalls(req),
	}
}

func codexClientMetadata(installationID, windowID string) map[string]string {
	out := map[string]string{}
	if v := strings.TrimSpace(installationID); v != "" {
		out["x-codex-installation-id"] = v
	}
	if v := strings.TrimSpace(windowID); v != "" {
		out["x-codex-window-id"] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func codexRequestInclude(requested []string, reasoningEnabled bool) []string {
	if len(requested) == 0 && !reasoningEnabled {
		return nil
	}
	seen := make(map[string]struct{}, len(requested)+1)
	out := make([]string, 0, len(requested)+1)
	for _, item := range requested {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if reasoningEnabled {
		const encryptedReasoning = "reasoning.encrypted_content"
		if _, ok := seen[encryptedReasoning]; !ok {
			out = append(out, encryptedReasoning)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func effectiveCodexReasoning(req ChatRequest, effort string) *codexReasoning {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		effort = strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	}
	if effort == "" && req.Reasoning != nil {
		effort = strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
	}
	var out codexReasoning
	switch effort {
	case EffortLow, EffortMedium, EffortHigh:
		out.Effort = effort
	}
	if req.Reasoning != nil {
		switch strings.ToLower(strings.TrimSpace(req.Reasoning.Summary)) {
		case "auto", "detailed", "none":
			out.Summary = strings.ToLower(strings.TrimSpace(req.Reasoning.Summary))
		}
	}
	if out.Effort == "" && out.Summary == "" {
		return nil
	}
	return &out
}

func effectiveCodexAppEffort(req ChatRequest) any {
	if r := effectiveCodexReasoning(req, ""); r != nil && r.Effort != "" {
		return r.Effort
	}
	return nil
}

func effectiveCodexAppSummary(req ChatRequest) any {
	if r := effectiveCodexReasoning(req, ""); r != nil && r.Summary != "" {
		return r.Summary
	}
	return nil
}

func parseCodexSSE(
	body io.Reader,
	renderer *tooltrans.EventRenderer,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 1024*128), 1024*1024*8)

	var eventName string
	var dataLines []string
	out := codexRunResult{FinishReason: "stop"}
	toolCallsByItemID := make(map[string]*codexToolCallState)
	nextToolIndex := 0
	emitToolCall := func(state *codexToolCallState, fn tooltrans.OpenAIToolCallFunction) error {
		if state == nil {
			return nil
		}
		return emitCodexRendered(renderer, tooltrans.Event{
			Kind: tooltrans.EventToolCallDelta,
			ToolCalls: []tooltrans.OpenAIToolCall{{
				Index:    state.Index,
				ID:       state.CallID,
				Type:     state.Type,
				Function: fn,
			}},
		}, emit, nil)
	}
	getToolState := func(itemID, callID, name string) (*codexToolCallState, bool) {
		itemID = strings.TrimSpace(itemID)
		callID = strings.TrimSpace(callID)
		if itemID == "" {
			itemID = callID
		}
		if callID == "" {
			callID = itemID
		}
		if state := toolCallsByItemID[itemID]; state != nil {
			if state.CallID == "" {
				state.CallID = callID
			}
			if state.Name == "" {
				state.Name = name
			}
			if callID != "" {
				toolCallsByItemID[callID] = state
			}
			return state, false
		}
		if callID != "" {
			if state := toolCallsByItemID[callID]; state != nil {
				if itemID != "" {
					toolCallsByItemID[itemID] = state
				}
				if state.Name == "" {
					state.Name = name
				}
				return state, false
			}
		}
		state := &codexToolCallState{
			Index:  nextToolIndex,
			ItemID: itemID,
			CallID: callID,
			Name:   name,
			Type:   "function",
		}
		nextToolIndex++
		if itemID != "" {
			toolCallsByItemID[itemID] = state
		}
		if callID != "" {
			toolCallsByItemID[callID] = state
		}
		return state, true
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			if eventName == "" || len(dataLines) == 0 {
				eventName = ""
				dataLines = nil
				continue
			}
			payload := strings.Join(dataLines, "\n")
			eventNameLocal := eventName
			eventName = ""
			dataLines = nil

			if strings.TrimSpace(payload) == "[DONE]" {
				break
			}
			var raw map[string]any
			if err := json.Unmarshal([]byte(payload), &raw); err != nil {
				continue
			}

			if eventNameLocal == "response.output_text.delta" {
				if delta, _ := raw["delta"].(string); delta != "" {
					if err := emitCodexRendered(renderer, tooltrans.Event{
						Kind: tooltrans.EventAssistantTextDelta,
						Text: delta,
					}, emit, nil); err != nil {
						return out, err
					}
				}
				continue
			}

			if eventNameLocal == "response.output_item.added" || eventNameLocal == "response.output_item.done" {
				item, _ := raw["item"].(map[string]any)
				itemType, _ := item["type"].(string)
				if itemType == "function_call" {
					itemID := strings.TrimSpace(codexMapString(item, "id"))
					callID := strings.TrimSpace(codexMapString(item, "call_id"))
					if itemID == "" {
						itemID = callID
					}
					if callID == "" {
						callID = itemID
					}
					name := strings.TrimSpace(codexMapString(item, "name"))
					args := codexMapString(item, "arguments")
					cursorName := codexInboundToolName(name)
					state, created := getToolState(itemID, callID, cursorName)
					if state.NativeName == "" {
						state.NativeName = name
					}
					if created {
						if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Name: state.Name}); err != nil {
							return out, err
						}
					}
					if state.Name == "" && name != "" {
						state.Name = codexInboundToolName(name)
					}
					out.FinishReason = "tool_calls"
					if eventNameLocal == "response.output_item.done" && state.NativeName == "shell_command" {
						if args == "" {
							args = state.Arguments.String()
						}
						if converted, ok := codexShellArgsFromShellCommandArguments(args); ok && !state.ArgumentsEmitted {
							if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Arguments: converted}); err != nil {
								return out, err
							}
							state.ArgumentsEmitted = true
						} else if !state.ArgumentsEmitted {
							logCodexToolingEvent(nil, context.Background(), "", "shell_command.parse_failed",
								slog.String("item_type", itemType),
								slog.String("item_id", itemID),
								slog.String("tool_name", "Shell"),
							)
						}
						continue
					}
					if eventNameLocal == "response.output_item.done" && args != "" {
						// Cursor assembles tool arguments by concatenating streamed deltas.
						// If we already emitted response.function_call_arguments.delta pieces,
						// emitting the full arguments again on output_item.done duplicates the
						// JSON buffer and leaves the tool call in an "attempted" state.
						if !state.ArgumentDeltaSeen {
							if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Arguments: args}); err != nil {
								return out, err
							}
							state.ArgumentsEmitted = true
						}
					}
				} else if itemType == "local_shell_call" {
					itemID := strings.TrimSpace(codexMapString(item, "id"))
					callID := strings.TrimSpace(codexMapString(item, "call_id"))
					state, created := getToolState(itemID, callID, "Shell")
					if created {
						if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Name: "Shell"}); err != nil {
							return out, err
						}
					}
					out.FinishReason = "tool_calls"
					if args, ok := codexShellArgsFromLocalShellItem(item); ok && !state.ArgumentsEmitted {
						if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Arguments: args}); err != nil {
							return out, err
						}
						state.ArgumentsEmitted = true
					} else if eventNameLocal == "response.output_item.done" && !state.ArgumentsEmitted {
						logCodexToolingEvent(nil, context.Background(), "", "native_local_shell.parse_failed",
							slog.String("item_type", itemType),
							slog.String("item_id", itemID),
							slog.String("tool_name", "Shell"),
						)
					}
				} else if itemType == "custom_tool_call" {
					itemID := strings.TrimSpace(codexMapString(item, "id"))
					callID := strings.TrimSpace(codexMapString(item, "call_id"))
					name := codexMapString(item, "name")
					cursorName := codexInboundToolName(name)
					if codexIsApplyPatchToolName(cursorName) || codexIsApplyPatchToolName(name) {
						cursorName = "ApplyPatch"
					}
					state, created := getToolState(itemID, callID, cursorName)
					if created {
						if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Name: state.Name}); err != nil {
							return out, err
						}
					}
					out.FinishReason = "tool_calls"
					input := codexRawString(item, "input")
					if input == "" {
						input = state.Input.String()
					}
					if args, ok := codexApplyPatchArgs(input); ok && !state.ArgumentsEmitted {
						if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Arguments: args}); err != nil {
							return out, err
						}
						state.ArgumentsEmitted = true
					} else if eventNameLocal == "response.output_item.done" && !state.ArgumentsEmitted {
						logCodexToolingEvent(nil, context.Background(), "", "native_custom_tool.parse_failed",
							slog.String("item_type", itemType),
							slog.String("item_id", itemID),
							slog.String("tool_name", cursorName),
						)
					}
				}
				continue
			}

			if eventNameLocal == "response.function_call_arguments.delta" {
				itemID := strings.TrimSpace(codexMapString(raw, "item_id"))
				delta := codexMapString(raw, "delta")
				state := toolCallsByItemID[itemID]
				if state != nil && delta != "" {
					state.ArgumentDeltaSeen = true
					out.FinishReason = "tool_calls"
					if state.NativeName == "shell_command" {
						state.Arguments.WriteString(delta)
						continue
					}
					if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Arguments: delta}); err != nil {
						return out, err
					}
				}
				continue
			}

			if eventNameLocal == "response.custom_tool_call_input.delta" {
				itemID := strings.TrimSpace(codexMapString(raw, "item_id"))
				callID := strings.TrimSpace(codexMapString(raw, "call_id"))
				delta := codexRawString(raw, "delta")
				state, created := getToolState(itemID, callID, "ApplyPatch")
				if created {
					if err := emitToolCall(state, tooltrans.OpenAIToolCallFunction{Name: state.Name}); err != nil {
						return out, err
					}
				}
				if delta != "" {
					state.Input.WriteString(delta)
					out.FinishReason = "tool_calls"
				}
				continue
			}

			if strings.Contains(eventNameLocal, "reasoning") && strings.HasSuffix(eventNameLocal, ".delta") {
				if delta, _ := raw["delta"].(string); delta != "" {
					kind := "text"
					var summaryIdx *int
					if strings.Contains(eventNameLocal, "summary") {
						kind = "summary"
						if v, ok := raw["summary_index"].(float64); ok {
							idx := int(v)
							summaryIdx = &idx
						}
					}
					if err := emitCodexRendered(renderer, tooltrans.Event{
						Kind:          tooltrans.EventReasoningDelta,
						Text:          delta,
						ReasoningKind: kind,
						SummaryIndex:  summaryIdx,
					}, emit, nil); err != nil {
						return codexRunResult{}, err
					}
				}
				continue
			}

			if eventNameLocal == "response.completed" {
				var c codexCompleted
				b, _ := json.Marshal(raw)
				if err := json.Unmarshal(b, &c); err == nil {
					out.Usage = mapCodexUsage(c)
				}
				out.ReasoningSignaled = codexReasoningTokens(raw) > 0
				if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, nil); err != nil {
					return out, err
				}
				state := renderer.State()
				out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
				out.ReasoningVisible = state.ReasoningVisible
				return out, nil
			}
			if eventNameLocal == "response.failed" {
				_ = emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, nil)
				msg := "codex response failed"
				if e, ok := raw["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok && m != "" {
						msg = m
					}
				}
				return out, fmt.Errorf("%s", msg)
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	state := renderer.State()
	out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
	out.ReasoningVisible = state.ReasoningVisible
	return out, nil
}

func (s *Server) runCodexDirect(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	token, err := s.readCodexAccessToken()
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	payload := buildCodexRequest(req, model, effort)
	conversationID := strings.TrimSpace(payload.PromptCache)
	windowID := ""
	if conversationID != "" {
		windowID = conversationID + ":0"
		payload.ClientMetadata = codexClientMetadata(s.readCodexAccountID(), windowID)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.codexBaseURL(), bytes.NewReader(raw))
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if conversationID != "" {
		httpReq.Header.Set("x-client-request-id", conversationID)
		httpReq.Header.Set("session_id", conversationID)
		if windowID != "" {
			httpReq.Header.Set("x-codex-window-id", windowID)
		}
	}
	slog.InfoContext(ctx, "adapter.codex.direct.request_prepared",
		"component", "adapter",
		"subcomponent", "codex",
		"request_id", reqID,
		"alias", model.Alias,
		"model", payload.Model,
		"input_count", len(payload.Input),
		"tool_count", len(payload.Tools),
		"has_prompt_cache_key", conversationID != "",
		"has_client_metadata", len(payload.ClientMetadata) > 0,
		"has_session_id_header", conversationID != "",
	)
	nativeShellCount, nativeCustomCount, functionToolCount := codexToolSpecCounts(payload.Tools)
	slog.InfoContext(ctx, "adapter.codex.direct.tools_prepared",
		"component", "adapter",
		"subcomponent", "codex",
		"request_id", reqID,
		"backend", "openai-codex",
		"alias", model.Alias,
		"model", payload.Model,
		"native_local_shell_count", nativeShellCount,
		"native_custom_count", nativeCustomCount,
		"function_tool_count", functionToolCount,
		"tool_count", len(payload.Tools),
	)

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return codexRunResult{FinishReason: "stop"}, fmt.Errorf("codex backend %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	renderer := tooltrans.NewEventRenderer(reqID, model.Alias, "codex", slog.Default())
	return parseCodexSSE(resp.Body, renderer, emit)
}

var codexWriteIntentWord = regexp.MustCompile(`(?i)\b(write|save|create|edit|update|modify|patch|apply|commit)\b`)

func codexChunksContainToolCalls(chunks []tooltrans.OpenAIStreamChunk) bool {
	for _, ch := range chunks {
		for _, choice := range ch.Choices {
			if len(choice.Delta.ToolCalls) > 0 {
				return true
			}
		}
	}
	return false
}

type codexAssembledToolCall struct {
	Name      string
	Arguments strings.Builder
}

func codexAssembleToolCalls(chunks []tooltrans.OpenAIStreamChunk) map[string]*codexAssembledToolCall {
	calls := make(map[string]*codexAssembledToolCall)
	for _, ch := range chunks {
		for _, choice := range ch.Choices {
			for _, tc := range choice.Delta.ToolCalls {
				key := tc.ID
				if key == "" {
					key = fmt.Sprintf("index:%d", tc.Index)
				}
				call := calls[key]
				if call == nil {
					call = &codexAssembledToolCall{}
					calls[key] = call
				}
				if tc.Function.Name != "" {
					call.Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					call.Arguments.WriteString(tc.Function.Arguments)
				}
			}
		}
	}
	return calls
}

func codexToolCallsHaveUsableArguments(chunks []tooltrans.OpenAIStreamChunk) bool {
	for _, call := range codexAssembleToolCalls(chunks) {
		args := strings.TrimSpace(call.Arguments.String())
		if args == "" || args == "{}" || args == "null" {
			continue
		}
		if codexIsApplyPatchToolName(call.Name) && strings.HasPrefix(args, "*** Begin Patch") {
			return true
		}
		var decoded any
		if err := json.Unmarshal([]byte(args), &decoded); err != nil {
			continue
		}
		switch v := decoded.(type) {
		case map[string]any:
			if len(v) == 0 {
				continue
			}
		case nil:
			continue
		}
		return true
	}
	return false
}

func codexCollectAssistantText(chunks []tooltrans.OpenAIStreamChunk) string {
	var b strings.Builder
	for _, ch := range chunks {
		for _, choice := range ch.Choices {
			if choice.Delta.Content != "" {
				b.WriteString(choice.Delta.Content)
			}
		}
	}
	return b.String()
}

func codexHasWriteIntent(req ChatRequest) bool {
	if len(req.Tools) == 0 {
		return false
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if !strings.EqualFold(req.Messages[i].Role, "user") {
			continue
		}
		text := strings.TrimSpace(FlattenContent(req.Messages[i].Content))
		return codexWriteIntentWord.MatchString(text)
	}
	return false
}

func codexLooksLikePathClarification(text string) bool {
	text = strings.ToLower(tooltrans.StripThinkingSentinel(text))
	phrases := []string{
		"where would you like the markdown file saved",
		"need to know the filename",
		"need to know the destination path",
		"clarification on the path",
		"save path",
		"destination path",
		"workspace root",
	}
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func codexShouldEscalateDirect(req ChatRequest, chunks []tooltrans.OpenAIStreamChunk, res codexRunResult) (bool, string) {
	if !codexHasWriteIntent(req) {
		return false, ""
	}
	if res.FinishReason == "tool_calls" || codexChunksContainToolCalls(chunks) {
		if !codexToolCallsHaveUsableArguments(chunks) {
			return true, "write_intent_empty_tool_arguments"
		}
		return false, ""
	}
	text := codexCollectAssistantText(chunks)
	if strings.TrimSpace(text) == "" {
		return true, "write_intent_without_tool_calls"
	}
	lower := strings.ToLower(tooltrans.StripThinkingSentinel(text))
	if strings.Contains(lower, "using the shell") || strings.Contains(lower, "using glob") || strings.Contains(lower, "let’s run `ls`") || strings.Contains(lower, "let's run `ls`") {
		return true, "write_intent_without_tool_calls"
	}
	if codexLooksLikePathClarification(text) {
		return true, "write_intent_without_tool_calls"
	}
	return true, "write_intent_without_tool_calls"
}

type codexRPCMsg struct {
	ID     any             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *codexRPCClient) send(id int, method string, params any) error {
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.stdin, string(raw)+"\n")
	return err
}

func (c *codexRPCClient) next() (codexRPCMsg, error) {
	line, err := c.stdout.ReadString('\n')
	if err != nil {
		return codexRPCMsg{}, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return codexRPCMsg{}, io.EOF
	}
	var msg codexRPCMsg
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return codexRPCMsg{}, err
	}
	return msg, nil
}

func rpcIDEquals(v any, want int) bool {
	switch id := v.(type) {
	case float64:
		return int(id) == want
	case int:
		return id == want
	case string:
		return id == strconv.Itoa(want)
	default:
		return false
	}
}

func (s *Server) runCodexAppFallback(
	ctx context.Context,
	req ChatRequest,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	cctx, cancel := context.WithTimeout(ctx, s.codexAppFallbackTimeout())
	defer cancel()
	rpc, err := startCodexRPC(cctx, s.codexAppServerPath())
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	defer func() {
		_ = rpc.stdin.Close()
		_ = rpc.cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, rpc.stdout)
	}()

	waitFor := func(id int) (codexRPCMsg, error) {
		for {
			msg, err := rpc.next()
			if err != nil {
				return codexRPCMsg{}, err
			}
			if msg.ID == nil || !rpcIDEquals(msg.ID, id) {
				continue
			}
			if msg.Error != nil {
				return codexRPCMsg{}, fmt.Errorf("codex rpc %s", msg.Error.Message)
			}
			return msg, nil
		}
	}

	if err := rpc.send(1, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "clyde-adapter",
			"title":   "Clyde Adapter",
			"version": "0.1.0",
		},
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	if _, err := waitFor(1); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	rawInit, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	if _, err := io.WriteString(rpc.stdin, string(rawInit)+"\n"); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}

	system, prompt := BuildPrompt(req.Messages)
	threadID := ""
	if err := rpc.send(2, "thread/start", map[string]any{
		"cwd":              ".",
		"approvalPolicy":   "never",
		"ephemeral":        true,
		"model":            strings.TrimSpace(req.Model),
		"reasoningEffort":  effectiveCodexAppEffort(req),
		"reasoningSummary": effectiveCodexAppSummary(req),
		"systemPrompt":     strings.TrimSpace(system),
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	threadMsg, err := waitFor(2)
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	if len(threadMsg.Result) > 0 {
		var r struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(threadMsg.Result, &r)
		threadID = strings.TrimSpace(r.ThreadID)
	}
	defer func() {
		if threadID == "" {
			return
		}
		_ = rpc.send(9, "thread/archive", map[string]any{"threadId": threadID})
	}()

	if err := rpc.send(3, "turn/start", map[string]any{
		"threadId":       threadID,
		"approvalPolicy": "never",
		"effort":         effectiveCodexAppEffort(req),
		"summary":        effectiveCodexAppSummary(req),
		"input": []map[string]any{{
			"type": "text",
			"text": sanitizeForUpstreamCache(prompt),
		}},
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}

	out := codexRunResult{FinishReason: "stop"}
	renderer := tooltrans.NewEventRenderer(reqID, req.Model, "codex", s.log)
	for {
		msg, err := rpc.next()
		if err != nil {
			return out, err
		}
		if msg.ID != nil && rpcIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, fmt.Errorf("codex turn/start: %s", msg.Error.Message)
			}
			continue
		}
		logAdapterProtocolEvent(ctx, reqID, "codex", msg.Method, slog.Int("params_bytes", len(msg.Params)))
		switch msg.Method {
		case "item/agentMessage/delta":
			var p struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.Int("delta_len", len(p.Delta)))
			if p.Delta != "" {
				if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventAssistantTextDelta, Text: p.Delta}, emit, nil); err != nil {
					return out, err
				}
			}
		case "turn/plan/updated":
			var p struct {
				Explanation string `json:"explanation"`
				Plan        []struct {
					Step   string `json:"step"`
					Status string `json:"status"`
				} `json:"plan"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			plan := make([]map[string]string, 0, len(p.Plan))
			for _, step := range p.Plan {
				plan = append(plan, map[string]string{"step": step.Step, "status": step.Status})
			}
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.Int("plan_steps", len(plan)), slog.Bool("has_explanation", strings.TrimSpace(p.Explanation) != ""))
			if ev, ok := codexPlanEvent(p.Explanation, plan); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/started", "item/completed":
			var p struct {
				Item map[string]any `json:"item"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_type", codexItemType(p.Item)), slog.String("item_status", codexItemStatus(p.Item)))
			if ev, ok := codexLifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
			var p struct {
				Delta  string `json:"delta"`
				ItemID string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("delta_len", len(p.Delta)))
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, p.Delta); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/mcpToolCall/progress":
			var p struct {
				Message string `json:"message"`
				ItemID  string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("message_len", len(p.Message)))
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, p.Message); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/fileChange/patchUpdated":
			var p struct {
				ItemID  string `json:"itemId"`
				Changes []any  `json:"changes"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("change_count", len(p.Changes)))
			changeCount := len(p.Changes)
			if changeCount < 1 {
				changeCount = 1
			}
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, fmt.Sprintf("Patch updated for %d file(s)", changeCount)); ok {
				ev.ChangeCount = changeCount
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct {
				SummaryIndex int `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			logCodexReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningSignaled}, emit, nil); err != nil {
				return out, err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p struct {
				Delta        string `json:"delta"`
				SummaryIndex int    `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			logCodexReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Int("delta_len", len(p.Delta)), slog.Bool("thinking_visible_before", renderer.State().ReasoningVisible))
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, nil); err != nil {
					return out, err
				}
			}
		case "turn/completed":
			if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, nil); err != nil {
				return out, err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			logCodexReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				logCodexToolingEvent(s.log, ctx, reqID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
			}
		}
	}
}

func (s *Server) collectCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	return adaptercodex.Collect(s, w, r, req, model, effort, reqID, started)
}

func (s *Server) streamCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	return adaptercodex.Stream(s, w, r, req, model, effort, reqID, started)
}

func (s *Server) dispatchCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string) {
	started := time.Now()
	if req.Stream {
		if err := s.streamCodex(w, r, req, model, effort, reqID, started); err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	}
	if err := s.collectCodex(w, r, req, model, effort, reqID, started); err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
	}
}
