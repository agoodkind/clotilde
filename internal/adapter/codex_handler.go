package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
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

type codexRunResult = adaptercodex.RunResult

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

func sanitizeForUpstreamCache(text string) string { return adaptercodex.SanitizeForUpstreamCache(text) }

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
	return adaptercodex.ClientMetadata(installationID, windowID)
}

func codexRequestInclude(requested []string, reasoningEnabled bool) []string {
	return adaptercodex.RequestInclude(requested, reasoningEnabled)
}

func effectiveCodexReasoning(req ChatRequest, effort string) *codexReasoning {
	return (*codexReasoning)(adaptercodex.EffectiveReasoning(req, effort))
}

func effectiveCodexAppEffort(req ChatRequest) any { return adaptercodex.EffectiveAppEffort(req) }

func effectiveCodexAppSummary(req ChatRequest) any { return adaptercodex.EffectiveAppSummary(req) }

func parseCodexSSE(body io.Reader, renderer *tooltrans.EventRenderer, emit func(tooltrans.OpenAIStreamChunk) error) (codexRunResult, error) {
	return adaptercodex.ParseSSE(body, renderer, emit)
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
