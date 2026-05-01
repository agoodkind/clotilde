package cursor

import "strings"

type Context struct {
	User           string
	RequestID      string
	ConversationID string
	WorkspacePath  string
}

func (c Context) StrongConversationKey() string {
	if strings.TrimSpace(c.ConversationID) == "" {
		return ""
	}
	return "cursor:" + strings.TrimSpace(c.ConversationID)
}
