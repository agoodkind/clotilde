package cursorctx

import (
	"encoding/json"
	"testing"
)

func TestFromOpenAIExtractsCursorIDs(t *testing.T) {
	raw := json.RawMessage(`{"cursorConversationId":"conv-123","cursorRequestId":"req-456"}`)
	got := FromOpenAI("user-1", raw)
	if got.User != "user-1" {
		t.Fatalf("User=%q", got.User)
	}
	if got.ConversationID != "conv-123" {
		t.Fatalf("ConversationID=%q", got.ConversationID)
	}
	if got.RequestID != "req-456" {
		t.Fatalf("RequestID=%q", got.RequestID)
	}
	if got.StrongConversationKey() != "cursor:conv-123" {
		t.Fatalf("StrongConversationKey=%q", got.StrongConversationKey())
	}
}

func TestFromOpenAIIgnoresMissingConversationID(t *testing.T) {
	got := FromOpenAI("user-1", json.RawMessage(`{"cursorRequestId":"req-456"}`))
	if got.StrongConversationKey() != "" {
		t.Fatalf("StrongConversationKey=%q", got.StrongConversationKey())
	}
}
