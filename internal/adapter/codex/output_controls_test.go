package codex

import (
    "encoding/json"
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
    header := BuildResponsesWebsocketHeaders("req-123", "token-abc")
    if got := header.Get("Authorization"); got != "Bearer token-abc" {
        t.Fatalf("Authorization=%q", got)
    }
    if got := header.Get("x-client-request-id"); got != "req-123" {
        t.Fatalf("x-client-request-id=%q", got)
    }
    if got := header.Get("OpenAI-Beta"); got != "responses_websockets=2026-02-06" {
        t.Fatalf("OpenAI-Beta=%q", got)
    }
}
