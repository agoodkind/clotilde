package cursor

import (
	"encoding/json"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

// Request is the Cursor-focused translation layer that sits on top of the
// OpenAI-compatible request shape. The root adapter still accepts OpenAI wire
// JSON, but the rest of the adapter can use this type when behavior depends on
// Cursor-specific conventions rather than generic OpenAI semantics.
type Request struct {
	OpenAI          adapteropenai.ChatRequest
	User            string
	RequestID       string
	ConversationID  string
	WorkspacePath   string
	NormalizedModel string
	RawToolNames    []string
	Mode            Mode
	CanSwitchMode   bool
	CanSpawnAgent   bool
	PathKind        RequestPathKind
}

// TranslateRequest derives Cursor-specific metadata from an OpenAI-compatible
// request without changing the underlying request payload.
func TranslateRequest(req adapteropenai.ChatRequest) Request {
	translated := Request{
		OpenAI:          req,
		User:            strings.TrimSpace(req.User),
		RequestID:       metadataString(req.Metadata, "cursorRequestId"),
		ConversationID:  metadataString(req.Metadata, "cursorConversationId"),
		WorkspacePath:   workspacePath(req),
		NormalizedModel: NormalizeModelAlias(req.Model),
	}
	translated.RawToolNames = rawToolNames(req)
	translated.Mode = requestMode(translated.RawToolNames)
	translated.CanSwitchMode = hasRawToolName(translated.RawToolNames, "SwitchMode")
	translated.CanSpawnAgent = hasRawToolName(translated.RawToolNames, "Subagent")
	translated.PathKind = RequestPath(translated)
	return translated
}

func (r Request) Context() Context {
	return Context{
		User:           r.User,
		RequestID:      r.RequestID,
		ConversationID: r.ConversationID,
		WorkspacePath:  r.WorkspacePath,
	}
}

func metadataString(raw json.RawMessage, keys ...string) string {
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

func rawToolNames(req adapteropenai.ChatRequest) []string {
	names := make([]string, 0, len(req.Tools)+len(req.Functions))
	for _, tool := range req.Tools {
		if name := strings.TrimSpace(tool.Function.Name); name != "" {
			names = append(names, name)
		}
	}
	for _, fn := range req.Functions {
		if name := strings.TrimSpace(fn.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func requestMode(toolNames []string) Mode {
	if hasRawToolName(toolNames, "CreatePlan") {
		return ModePlan
	}
	return ModeAgent
}
