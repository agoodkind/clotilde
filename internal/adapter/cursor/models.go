package cursor

import (
	"log/slog"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type RequestPathKind string

const (
	RequestPathForeground RequestPathKind = "foreground"
	RequestPathBackground RequestPathKind = "background"
	RequestPathResume     RequestPathKind = "resume"
	RequestPathSubagent   RequestPathKind = "subagent"
)

func NormalizeModelAlias(rawModel string) string {
	return strings.TrimSpace(rawModel)
}

func RequestPath(req Request) RequestPathKind {
	if metadataHasAny(req.Metadata, "cursorResumeTaskId", "resumeTaskId", "resume", "isResume") {
		return RequestPathResume
	}
	if metadataHasAny(req.Metadata, "cursorSubagentId", "subagentId", "subagent", "isSubagent") {
		return RequestPathSubagent
	}
	if metadataHasAny(req.Metadata, "cursorBackgroundTaskId", "backgroundTaskId", "background", "isBackground", "runInBackground") {
		return RequestPathBackground
	}
	if requestTextContains(req.OpenAI, "you are the forked subagent") {
		return RequestPathSubagent
	}
	if requestTextContains(req.OpenAI, "resume after background task", "background task completed") {
		return RequestPathResume
	}
	if requestTextContains(req.OpenAI, "background task") {
		return RequestPathBackground
	}
	return RequestPathForeground
}

func BoundaryLogAttrs(req Request, rawModel string, toolNames []string) []slog.Attr {
	normalizedModel := req.NormalizedModel
	if normalizedModel == "" {
		normalizedModel = NormalizeModelAlias(rawModel)
	}
	mode := DetectMode(req)
	if len(toolNames) == 0 {
		toolNames = req.RawToolNames
	}

	attrs := []slog.Attr{
		slog.String("cursor_request_path", string(req.PathKind)),
		slog.String("cursor_raw_model", strings.TrimSpace(rawModel)),
		slog.String("cursor_normalized_model", normalizedModel),
		slog.Any("cursor_raw_tool_names", toolNames),
		slog.String("cursor_mode", string(mode)),
		slog.Bool("cursor_can_spawn_agent", req.CanSpawnAgent),
		slog.Bool("cursor_can_switch_mode", req.CanSwitchMode),
	}

	if req.ConversationID != "" {
		attrs = append(attrs, slog.String("cursor_conversation_id", req.ConversationID))
	}
	if req.RequestID != "" {
		attrs = append(attrs, slog.String("cursor_request_id", req.RequestID))
	}

	return attrs
}

func hasRawToolName(toolNames []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, name := range toolNames {
		if strings.TrimSpace(name) == want {
			return true
		}
	}
	return false
}

func requestTextContains(req adapteropenai.ChatRequest, needles ...string) bool {
	if len(needles) == 0 {
		return false
	}
	haystack := strings.Builder{}
	for _, msg := range req.Messages {
		if text := adapteropenai.FlattenContent(msg.Content); text != "" {
			haystack.WriteString(text)
			haystack.WriteByte('\n')
		}
	}
	if len(req.Input) > 0 {
		haystack.Write(req.Input)
	}
	text := strings.ToLower(haystack.String())
	if text == "" {
		return false
	}
	for _, needle := range needles {
		if strings.Contains(text, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
