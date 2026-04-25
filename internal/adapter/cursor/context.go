package cursor

import (
	"encoding/json"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type Context struct {
	User           string
	RequestID      string
	ConversationID string
	WorkspacePath  string
}

func FromOpenAI(user string, metadata json.RawMessage) Context {
	return Context{
		User:           strings.TrimSpace(user),
		RequestID:      metadataString(metadata, "cursorRequestId"),
		ConversationID: metadataString(metadata, "cursorConversationId"),
	}
}

func FromRequest(req adapteropenai.ChatRequest) Context {
	ctx := FromOpenAI(req.User, req.Metadata)
	ctx.WorkspacePath = WorkspacePath(req)
	return ctx
}

func (c Context) StrongConversationKey() string {
	if strings.TrimSpace(c.ConversationID) == "" {
		return ""
	}
	return "cursor:" + strings.TrimSpace(c.ConversationID)
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
