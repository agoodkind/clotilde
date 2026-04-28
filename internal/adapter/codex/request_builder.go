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
	shellMode := ShellToolModeForModel(model)
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
	if rawInput, ok := inputFromResponsesInput(req.Input, shellMode, workspacePath, &systemSections, buildContextual); ok {
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
					input = append(input, FunctionCallItem(tc, shellMode))
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
		// Store=true tells ChatGPT Pro to persist the response so the
		// next turn can reference it via previous_response_id. Without
		// this, the upstream returns "Previous response with id ...
		// not found." on every continuation attempt and our ledger
		// burns CPU on a recovery retry that always falls back to
		// full-conversation replay. The continuation ledger is the
		// whole reason we send these requests over websocket; storing
		// the response is the prerequisite for it working.
		Store:                true,
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
		Tools:                toolSpecs(req, shellMode),
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
		if allowed[toolName] || KeepToolForWriteIntent(toolName) {
			filtered = append(filtered, tool)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return tools
}

func toolSpecs(req adapteropenai.ChatRequest, shellMode string) []any {
	tools := requestTools(req)
	if len(tools) == 0 {
		return nil
	}
	out := make([]any, 0, len(tools))
	emittedNativeShell := false
	emittedNativeApplyPatch := false
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
		out = append(out, FunctionToolSpec(OutboundToolName(toolName), tool.Function.Description, tool.Function.Parameters, tool.Function.Strict))
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

// rewriteWorkspacePath translates daemon-cwd-rooted paths to the
// caller's workspace path. Originally added for the codex subprocess
// path where the codex CLI ran in the daemon's cwd; the websocket
// direct path has no such translation need because Cursor and the
// upstream both speak in absolute paths anchored at the workspace.
//
// Several guards keep this from corrupting absolute paths when the
// daemon is launched without an explicit working directory:
//   - bail when workspacePath or text is empty.
//   - bail when GetwdFn returns the same path.
//   - bail when the daemon cwd is `/` (launchd default), because
//     replacing every `/` with the workspace mashes every absolute
//     path beyond recognition.
//   - bail when the daemon cwd does not actually appear in the text
//     as a directory-bounded substring; avoids partial-prefix
//     mashing on text that merely happens to contain a similar
//     short string.
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
	// Reject cwd values that are too short to be meaningful as a
	// path prefix. `/` is the launchd default and trips a
	// catastrophic global slash replace. Anything shorter than 2
	// characters or containing only path separators is treated as
	// "no real prefix".
	if len(cwd) < 2 || strings.Trim(cwd, "/") == "" {
		return text
	}
	// Replace only bounded occurrences. An unbounded ReplaceAll would
	// rewrite substrings inside unrelated identifiers (e.g. `/var`
	// inside `/variable`), which is the original mashing failure.
	return pathBoundedReplaceAll(text, cwd, workspacePath)
}

// pathBoundedContains reports whether `needle` appears in `haystack`
// at a position where it is bounded by the start of the string, the
// end of the string, or a non-identifier character. When `needle`
// begins with a path separator, the leading `/` itself counts as the
// preceding boundary so suffix matches like `/bar` in `/foo/bar`
// resolve correctly.
func pathBoundedContains(haystack, needle string) bool {
	_, found := pathBoundedFirstMatch(haystack, needle, 0)
	return found
}

// pathBoundedReplaceAll replaces every bounded occurrence of needle
// in haystack with replacement. Unbounded occurrences (substrings
// inside identifiers) pass through untouched.
func pathBoundedReplaceAll(haystack, needle, replacement string) string {
	if needle == "" {
		return haystack
	}
	var out strings.Builder
	out.Grow(len(haystack))
	idx := 0
	for idx < len(haystack) {
		pos, ok := pathBoundedFirstMatch(haystack, needle, idx)
		if !ok {
			out.WriteString(haystack[idx:])
			return out.String()
		}
		out.WriteString(haystack[idx:pos])
		out.WriteString(replacement)
		idx = pos + len(needle)
	}
	return out.String()
}

func pathBoundedFirstMatch(haystack, needle string, from int) (int, bool) {
	if needle == "" || from >= len(haystack) {
		return 0, false
	}
	for {
		rel := strings.Index(haystack[from:], needle)
		if rel < 0 {
			return 0, false
		}
		pos := from + rel
		end := pos + len(needle)
		if isPathBoundary(haystack, pos, end, needle) {
			return pos, true
		}
		from = pos + 1
		if from >= len(haystack) {
			return 0, false
		}
	}
}

func isPathBoundary(haystack string, pos, end int, needle string) bool {
	startOK := pos == 0 || isPathSeparatorByte(haystack[pos-1]) || needle[0] == '/'
	endOK := end == len(haystack) || isPathSeparatorByte(haystack[end])
	return startOK && endOK
}

func isPathSeparatorByte(b byte) bool {
	switch b {
	case '/', ' ', '\n', '\t', '"', '\'', '(', ')', ',', ':', ';':
		return true
	}
	return false
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
		"name":      OutboundToolName(tc.Function.Name),
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

func functionCallFromResponsesItem(item map[string]any, shellMode, workspacePath string) (map[string]any, string) {
	callID := mapString(item, "call_id")
	name := mapString(item, "name")
	args := rewriteWorkspacePath(rawString(item, "arguments"), workspacePath)
	tc := adapteropenai.ToolCall{
		ID:   callID,
		Type: "function",
		Function: adapteropenai.ToolCallFunction{
			Name:      InboundToolName(name),
			Arguments: args,
		},
	}
	return functionCallItem(tc, shellMode), tc.Function.Name
}

func inputFromResponsesInput(
	raw json.RawMessage,
	shellMode string,
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
			call, toolName := functionCallFromResponsesItem(item, shellMode, workspacePath)
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
				toolCallNames[callID] = InboundToolName(name)
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
