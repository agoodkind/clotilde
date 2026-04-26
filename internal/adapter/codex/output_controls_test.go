package codex

import (
	"encoding/json"
	"net/http"
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestBuildOutputControlsPassesThroughMaxCompletionTokens(t *testing.T) {
	maxCompletion := 4096
	controls := BuildOutputControls(adapteropenai.ChatRequest{MaxComplTokens: &maxCompletion})
	if controls.MaxCompletion == nil || *controls.MaxCompletion != maxCompletion {
		t.Fatalf("max_completion_tokens=%v want %d", controls.MaxCompletion, maxCompletion)
	}
}

func TestServiceTierFromMetadataMapsFastToPriority(t *testing.T) {
	if got := ServiceTierFromMetadata(json.RawMessage(`{"service_tier":"fast"}`)); got != "priority" {
		t.Fatalf("service_tier=%q want priority", got)
	}
}

func TestServiceTierFromMetadataPreservesFlex(t *testing.T) {
	if got := ServiceTierFromMetadata(json.RawMessage(`{"service_tier":"flex"}`)); got != "flex" {
		t.Fatalf("service_tier=%q want flex", got)
	}
}

func TestServiceTierFromMetadataIgnoresInvalidMetadata(t *testing.T) {
	if got := ServiceTierFromMetadata(json.RawMessage(`{bad`)); got != "" {
		t.Fatalf("service_tier=%q want empty", got)
	}
}

func TestBuildResponsesWebsocketHeadersIncludesCurrentParityHeaders(t *testing.T) {
	turnState := NewTurnState()
	_ = turnState.CaptureFromHeaders(mapHeader(CodexTurnStateHeader, "turn-123"))
	header := BuildResponsesWebsocketHeaders(ResponsesWebsocketHeaderConfig{
		RequestID:      "req-123",
		ConversationID: "cursor:conv-123",
		Token:          "token-abc",
		InstallationID: "acct-123",
		TurnState:      turnState,
	})
	if got := header.Get("Authorization"); got != "Bearer token-abc" {
		t.Fatalf("Authorization=%q", got)
	}
	if got := header.Get("x-client-request-id"); got != "cursor:conv-123" {
		t.Fatalf("x-client-request-id=%q", got)
	}
	if got := header.Get("session_id"); got != "cursor:conv-123" {
		t.Fatalf("session_id=%q", got)
	}
	if got := header.Get(CodexInstallationIDHeader); got != "acct-123" {
		t.Fatalf("%s=%q", CodexInstallationIDHeader, got)
	}
	if got := header.Get(CodexWindowIDHeader); got != "cursor:conv-123:0" {
		t.Fatalf("%s=%q", CodexWindowIDHeader, got)
	}
	if got := header.Get(CodexTurnStateHeader); got != "turn-123" {
		t.Fatalf("%s=%q", CodexTurnStateHeader, got)
	}
	if got := header.Get("OpenAI-Beta"); got != "responses_websockets=2026-02-06" {
		t.Fatalf("OpenAI-Beta=%q", got)
	}
}

func mapHeader(key, value string) http.Header {
	h := http.Header{}
	h.Set(key, value)
	return h
}
