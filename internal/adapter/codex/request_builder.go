package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

var (
	NowFunc     = time.Now
	GetwdFn     = func() (string, error) { return "", fmt.Errorf("codex GetwdFn not initialized") }
	ShellNameFn = func() string { return "sh" }
)

func EnvironmentContextText(workspacePath string) (string, bool) {
	cwd := strings.TrimSpace(workspacePath)
	if cwd == "" {
		var err error
		cwd, err = GetwdFn()
		if err != nil {
			return "", false
		}
	}
	now := NowFunc()
	return strings.TrimSpace(fmt.Sprintf(`
<environment_context>
  <cwd>%s</cwd>
  <shell>%s</shell>
  <current_date>%s</current_date>
  <timezone>%s</timezone>
</environment_context>`,
		cwd,
		ShellNameFn(),
		now.Format("2006-01-02"),
		now.Format("-07:00 MST"),
	)), true
}

func MessageContent(role, textType, text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": role,
		"content": []map[string]any{{
			"type": textType,
			"text": text,
		}},
	}
}

func BuildRequest(req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort string) HTTPTransportRequest {
	cursorReq := adaptercursor.TranslateRequest(req)
	promptContext := adaptercursor.CodexPromptContext(cursorReq, nil, "")
	input := make([]map[string]any, 0, len(req.Messages))
	systemSections := make([]string, 0, 8)
	contextualUserSections := make([]string, 0, 4)
	toolCallNames := make(map[string]string)
	modelName := strings.TrimSpace(model.ClaudeModel)
	if modelName == "" {
		modelName = model.Alias
	}
	lastUserIdx := -1
	for i, msg := range req.Messages {
		if strings.EqualFold(msg.Role, "user") {
			lastUserIdx = i
		}
	}
	workspacePath := cursorReq.WorkspacePath
	environmentText, hasEnvironment := EnvironmentContextText(workspacePath)
	if hasEnvironment {
		contextualUserSections = append(contextualUserSections, environmentText)
	}
	buildContextual := func() []map[string]any {
		promptContext = adaptercursor.CodexPromptContext(cursorReq, systemSections, strings.Join(contextualUserSections, "\n\n"))
		contextual := make([]map[string]any, 0, 2)
		if len(promptContext.DeveloperSections) > 0 {
			contextual = append(contextual, MessageContent("developer", "input_text", strings.Join(promptContext.DeveloperSections, "\n\n")))
		}
		if len(promptContext.UserSections) > 0 {
			contextual = append(contextual, MessageContent("user", "input_text", strings.Join(promptContext.UserSections, "\n\n")))
		}
		return contextual
	}
	if rawInput, ok := inputFromResponsesInput(req.Input, modelName, workspacePath, &systemSections, buildContextual); ok {
		input = rawInput
	} else {
		insertedContext := false
		for idx, msg := range req.Messages {
			text := strings.TrimSpace(SanitizeForUpstreamCache(adapteropenai.FlattenContent(msg.Content)))
			switch strings.ToLower(msg.Role) {
			case "system", "developer":
				if text != "" {
					systemSections = append(systemSections, text)
				}
				continue
			case "assistant":
				for _, tc := range msg.ToolCalls {
					if strings.TrimSpace(tc.Function.Name) == "" {
						continue
					}
					callID := strings.TrimSpace(tc.ID)
					if callID == "" {
						callID = fmt.Sprintf("call_%d", tc.Index)
					}
					toolCallNames[callID] = ToolCallName(tc)
					input = append(input, FunctionCallItem(tc, ShellToolMode(modelName)))
				}
				if text != "" {
					input = append(input, MessageContent("assistant", "output_text", text))
				}
			case "tool", "function":
				if text != "" && strings.TrimSpace(msg.ToolCallID) != "" {
					if IsApplyPatchToolName(toolCallNames[strings.TrimSpace(msg.ToolCallID)]) {
						input = append(input, CustomToolCallOutputItem(msg.ToolCallID, text))
					} else {
						input = append(input, FunctionCallOutputItem(msg.ToolCallID, text))
					}
				} else if text != "" {
					input = append(input, MessageContent("user", "input_text", "tool: "+text))
				}
			default:
				if !insertedContext && idx == lastUserIdx {
					if contextual := buildContextual(); len(contextual) > 0 {
						input = append(input, contextual...)
					}
					insertedContext = true
				}
				if text != "" {
					input = append(input, MessageContent("user", "input_text", text))
				}
			}
		}
		if !insertedContext {
			if contextual := buildContextual(); len(contextual) > 0 {
				input = append(input, contextual...)
			}
		}
	}
	instructions := BaseInstructions(modelName)
	if strings.TrimSpace(promptContext.InstructionPrefix) != "" {
		if instructions == "" {
			instructions = promptContext.InstructionPrefix
		} else {
			instructions = promptContext.InstructionPrefix + "\n\n" + instructions
		}
	}
	if len(input) == 0 {
		input = append(input, MessageContent("user", "input_text", " "))
	}
	reasoning := EffectiveReasoning(req, effort)
	include := RequestInclude(req.Include, reasoning != nil)
	outputControls := BuildOutputControls(req)
	return HTTPTransportRequest{
		Model:                modelName,
		Instructions:         instructions,
		Store:                false,
		Stream:               true,
		Include:              include,
		PromptCache:          requestContextTrackerKey(cursorReq, model.Alias),
		PromptCacheRetention: outputControls.PromptCacheRetention,
		ServiceTier:          ServiceTierFromRequest(req),
		Reasoning:            reasoning,
		MaxCompletion:        outputControls.MaxCompletion,
		Text:                 outputControls.Text,
		Truncation:           outputControls.Truncation,
		Input:                input,
		Tools:                toolSpecs(req, modelName),
		ToolChoice:           "auto",
		ParallelToolCalls:    parallelToolCalls(req),
	}
}

func parallelToolCalls(req adapteropenai.ChatRequest) bool {
	if req.ParallelTools == nil {
		return true
	}
	return *req.ParallelTools
}

func requestTools(req adapteropenai.ChatRequest) []adapteropenai.Tool {
	var tools []adapteropenai.Tool
	if len(req.Tools) > 0 {
		tools = append(tools, req.Tools...)
	} else if len(req.Functions) > 0 {
		for _, fn := range req.Functions {
			tools = append(tools, adapteropenai.Tool{
				Type: "function",
				Function: adapteropenai.ToolFunctionSchema{
					Name:        fn.Name,
					Description: fn.Description,
					Parameters:  fn.Parameters,
				},
			})
		}
	}
	if !HasWriteIntent(req) {
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
	filtered := make([]adapteropenai.Tool, 0, len(tools))
	for _, tool := range tools {
		toolName := strings.TrimSpace(tool.Function.Name)
		if allowed[toolName] || adaptercursor.KeepCodexToolForWriteIntent(toolName) {
			filtered = append(filtered, tool)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return tools
}

func toolSpecs(req adapteropenai.ChatRequest, modelName string) []any {
	tools := requestTools(req)
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	emittedNativeShell := false
	emittedNativeApplyPatch := false
	shellMode := ShellToolMode(modelName)
	for _, tool := range tools {
		toolName := strings.TrimSpace(tool.Function.Name)
		if IsShellToolName(toolName) {
			if !emittedNativeShell {
				switch shellMode {
				case "local_shell":
					out = append(out, NativeLocalShellSpec())
				case "shell_command":
					out = append(out, ShellCommandSpec())
				default:
					out = append(out, FunctionToolSpec("shell", tool.Function.Description, tool.Function.Parameters, tool.Function.Strict))
				}
				emittedNativeShell = true
			}
			continue
		}
		if IsApplyPatchToolName(toolName) {
			if !emittedNativeApplyPatch {
				out = append(out, NativeApplyPatchSpec())
				emittedNativeApplyPatch = true
			}
			continue
		}
		out = append(out, FunctionToolSpec(adaptercursor.OutboundCodexToolName(toolName), tool.Function.Description, tool.Function.Parameters, tool.Function.Strict))
	}
	return out
}

func responsesContentText(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return SanitizeForUpstreamCache(v)
	case []any:
		var parts []string
		for _, part := range v {
			m, _ := part.(map[string]any)
			if m == nil {
				continue
			}
			switch strings.TrimSpace(mapString(m, "type")) {
			case "text", "input_text", "output_text":
				if text := rawString(m, "text"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return SanitizeForUpstreamCache(strings.Join(parts, "\n"))
	case map[string]any:
		switch strings.TrimSpace(mapString(v, "type")) {
		case "text", "input_text", "output_text":
			return SanitizeForUpstreamCache(rawString(v, "text"))
		}
	}
	return ""
}

func responsesOutputText(raw any) string {
	text := responsesContentText(raw)
	if text != "" {
		return text
	}
	switch v := raw.(type) {
	case string:
		return SanitizeForUpstreamCache(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return SanitizeForUpstreamCache(string(b))
	}
}

func rewriteWorkspacePath(text, workspacePath string) string {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" || text == "" {
		return text
	}
	cwd, err := GetwdFn()
	if err != nil {
		return text
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || cwd == workspacePath {
		return text
	}
	return strings.ReplaceAll(text, cwd, workspacePath)
}

func functionCallItem(tc adapteropenai.ToolCall, shellMode string) map[string]any {
	if IsShellToolName(ToolCallName(tc)) {
		if shellMode == "shell_command" {
			return ShellCommandCallItem(tc)
		}
		return LocalShellCallItem(tc, ShellNameFn())
	}
	if IsApplyPatchToolName(ToolCallName(tc)) {
		return ApplyPatchCallItem(tc)
	}
	callID := strings.TrimSpace(tc.ID)
	if callID == "" {
		callID = fmt.Sprintf("call_%d", tc.Index)
	}
	return map[string]any{
		"type":      "function_call",
		"call_id":   callID,
		"name":      adaptercursor.OutboundCodexToolName(tc.Function.Name),
		"arguments": tc.Function.Arguments,
	}
}

func FunctionCallItem(tc adapteropenai.ToolCall, shellMode string) map[string]any {
	return functionCallItem(tc, shellMode)
}

func FunctionCallOutputItem(callID, text string) map[string]any {
	return map[string]any{
		"type":    "function_call_output",
		"call_id": strings.TrimSpace(callID),
		"output":  text,
	}
}

func functionCallFromResponsesItem(item map[string]any, modelName, workspacePath string) (map[string]any, string) {
	callID := mapString(item, "call_id")
	name := mapString(item, "name")
	args := rewriteWorkspacePath(rawString(item, "arguments"), workspacePath)
	tc := adapteropenai.ToolCall{
		ID:   callID,
		Type: "function",
		Function: adapteropenai.ToolCallFunction{
			Name:      adaptercursor.InboundCodexToolName(name),
			Arguments: args,
		},
	}
	return functionCallItem(tc, ShellToolMode(modelName)), tc.Function.Name
}

func inputFromResponsesInput(
	raw json.RawMessage,
	modelName string,
	workspacePath string,
	developerSections *[]string,
	buildContextual func() []map[string]any,
) ([]map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, false
	}
	lastUserIdx := -1
	for i, item := range items {
		if strings.EqualFold(mapString(item, "role"), "user") {
			lastUserIdx = i
		}
	}
	input := make([]map[string]any, 0, len(items)+2)
	toolCallNames := make(map[string]string)
	insertedContext := false
	for idx, item := range items {
		role := strings.ToLower(mapString(item, "role"))
		itemType := strings.TrimSpace(mapString(item, "type"))
		switch {
		case role == "system" || role == "developer":
			if text := strings.TrimSpace(responsesContentText(item["content"])); text != "" {
				*developerSections = append(*developerSections, text)
			}
		case role == "user":
			if !insertedContext && idx == lastUserIdx {
				if contextual := buildContextual(); len(contextual) > 0 {
					input = append(input, contextual...)
				}
				insertedContext = true
			}
			if text := strings.TrimSpace(responsesContentText(item["content"])); text != "" {
				input = append(input, MessageContent("user", "input_text", text))
			}
		case role == "assistant":
			if text := strings.TrimSpace(responsesContentText(item["content"])); text != "" {
				input = append(input, MessageContent("assistant", "output_text", text))
			}
		case itemType == "function_call":
			call, toolName := functionCallFromResponsesItem(item, modelName, workspacePath)
			if callID := mapString(item, "call_id"); callID != "" {
				toolCallNames[callID] = toolName
			}
			input = append(input, call)
		case itemType == "function_call_output":
			callID := mapString(item, "call_id")
			output := strings.TrimSpace(rewriteWorkspacePath(responsesOutputText(item["output"]), workspacePath))
			if output == "" {
				continue
			}
			if IsApplyPatchToolName(toolCallNames[callID]) {
				input = append(input, CustomToolCallOutputItem(callID, output))
			} else {
				input = append(input, FunctionCallOutputItem(callID, output))
			}
		case itemType == "custom_tool_call":
			callID := mapString(item, "call_id")
			name := mapString(item, "name")
			inputText := rewriteWorkspacePath(UnwrapApplyPatchInput(rawString(item, "input")), workspacePath)
			if callID != "" {
				toolCallNames[callID] = adaptercursor.InboundCodexToolName(name)
			}
			input = append(input, map[string]any{
				"type":    "custom_tool_call",
				"call_id": callID,
				"name":    name,
				"input":   inputText,
			})
		case itemType == "custom_tool_call_output":
			callID := mapString(item, "call_id")
			output := strings.TrimSpace(rewriteWorkspacePath(responsesOutputText(item["output"]), workspacePath))
			if output != "" {
				input = append(input, CustomToolCallOutputItem(callID, output))
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

func requestContextTrackerKey(req adaptercursor.Request, modelAlias string) string {
	if cursor := req.Context(); cursor.StrongConversationKey() != "" {
		return cursor.StrongConversationKey()
	}
	if v := strings.TrimSpace(req.User); v != "" {
		return "user:" + v
	}
	if v := requestMetadataString(req.OpenAI.Metadata, "conversation_id", "conversationId", "composerId", "composer_id", "thread_id", "threadId", "chat_id", "chatId"); v != "" {
		return "meta:" + v
	}
	firstUser := ""
	for _, msg := range req.OpenAI.Messages {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			firstUser = strings.TrimSpace(adapteropenai.FlattenContent(msg.Content))
			if firstUser != "" {
				break
			}
		}
	}
	if firstUser == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(modelAlias + "\n" + firstUser))
	return "fingerprint:" + hex.EncodeToString(sum[:16])
}

func requestMetadataString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
