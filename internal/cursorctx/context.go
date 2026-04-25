package cursorctx

import (
	"encoding/json"
	"strings"
)

// Context carries Cursor-specific request metadata in a transport-agnostic form.
// It is intentionally small so other packages can reuse it for logging, stats,
// and session keying without depending on adapter-specific request types.
type Context struct {
	User           string
	RequestID      string
	ConversationID string
}

func FromOpenAI(user string, metadata json.RawMessage) Context {
	return Context{
		User:           strings.TrimSpace(user),
		RequestID:      metadataString(metadata, "cursorRequestId"),
		ConversationID: metadataString(metadata, "cursorConversationId"),
	}
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
