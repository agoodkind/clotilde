package cursor

import (
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type Context struct {
	User           string
	RequestID      string
	ConversationID string
	WorkspacePath  string
}

func FromRequest(req adapteropenai.ChatRequest) Context {
	return TranslateRequest(req).Context()
}

func FromTranslatedRequest(req Request) Context { return req.Context() }

func (c Context) StrongConversationKey() string {
	if strings.TrimSpace(c.ConversationID) == "" {
		return ""
	}
	return "cursor:" + strings.TrimSpace(c.ConversationID)
}
