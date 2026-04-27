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
	Metadata        map[string]json.RawMessage
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
		Metadata:        metadataMap(req.Metadata),
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
	m := metadataMap(raw)
	if len(m) == 0 {
		return ""
	}
	for _, key := range keys {
		if v, ok := m[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func metadataMap(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func metadataHasAny(m map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		var asBool bool
		if json.Unmarshal(raw, &asBool) == nil {
			if asBool {
				return true
			}
			continue
		}
		var asString string
		if json.Unmarshal(raw, &asString) == nil {
			trimmed := strings.TrimSpace(asString)
			if trimmed != "" && trimmed != "false" {
				return true
			}
			continue
		}
		var asNumber float64
		if json.Unmarshal(raw, &asNumber) == nil {
			if asNumber != 0 {
				return true
			}
			continue
		}
		if string(raw) != "null" {
			return true
		}
	}
	return false
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
