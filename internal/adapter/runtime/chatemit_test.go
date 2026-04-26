package runtime

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestEmitUsageChunk(t *testing.T) {
	t.Parallel()
	var got StreamChunk
	u := Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}
	err := EmitUsageChunk(func(sc StreamChunk) error {
		got = sc
		return nil
	}, "req-1", "alias", 123, u)
	if err != nil {
		t.Fatalf("EmitUsageChunk returned err: %v", err)
	}
	if got.ID != "req-1" || got.Model != "alias" || got.Created != 123 {
		t.Fatalf("unexpected chunk header: %+v", got)
	}
	if len(got.Choices) != 0 {
		t.Fatalf("expected no choices, got %d", len(got.Choices))
	}
	if got.Usage == nil || got.Usage.PromptTokens != 2 {
		t.Fatalf("usage missing or wrong: %+v", got.Usage)
	}
}

func TestEmitFinishChunk(t *testing.T) {
	t.Parallel()
	var got StreamChunk
	err := EmitFinishChunk(func(sc StreamChunk) error {
		got = sc
		return nil
	}, "req-1", "alias", 456, "stop")
	if err != nil {
		t.Fatalf("EmitFinishChunk returned err: %v", err)
	}
	if got.ID != "req-1" || got.Model != "alias" || got.Created != 456 {
		t.Fatalf("unexpected chunk header: %+v", got)
	}
	if len(got.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(got.Choices))
	}
	if got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "stop" {
		t.Fatalf("expected finish_reason stop, got %+v", got.Choices[0].FinishReason)
	}
}

func TestBuildAssistantMessageWithToolCallsUsesNullContent(t *testing.T) {
	t.Parallel()
	msg := BuildAssistantMessage(AssistantMessageParts{
		ToolCalls: []ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: ToolCallFunction{
				Name:      "Read",
				Arguments: `{"path":"README.md"}`,
			},
		}},
	})
	if string(msg.Content) != "null" {
		t.Fatalf("content = %s want null", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "Read" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
}

func TestBuildAssistantMessageWithTextQuotesJSON(t *testing.T) {
	t.Parallel()
	msg := BuildAssistantMessage(AssistantMessageParts{Text: "hello\nworld"})
	var text string
	if err := json.Unmarshal(msg.Content, &text); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if text != "hello\nworld" {
		t.Fatalf("text = %q", text)
	}
}

func TestBuildChatCompletion(t *testing.T) {
	t.Parallel()
	resp := BuildChatCompletion(
		"req-1",
		"alias",
		"fp",
		BuildAssistantMessage(AssistantMessageParts{Text: "ok"}),
		"stop",
		Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	)
	if resp.ID != "req-1" || resp.Model != "alias" || resp.SystemFingerprint != "fp" {
		t.Fatalf("unexpected response header: %+v", resp)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected choices: %+v", resp.Choices)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 3 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestBuildDeltaChunk(t *testing.T) {
	t.Parallel()
	chunk := BuildDeltaChunk("req-1", "alias", 123, StreamDelta{Role: "assistant", Content: "hi"})
	if chunk.ID != "req-1" || chunk.Model != "alias" || chunk.Created != 123 {
		t.Fatalf("unexpected chunk header: %+v", chunk)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Delta.Content != "hi" {
		t.Fatalf("unexpected choices: %+v", chunk.Choices)
	}
}

func TestNoticeForResponseHeadersSuccess(t *testing.T) {
	t.Parallel()
	resp := ChatResponse{
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Content: json.RawMessage(`"hello"`),
			},
		}},
	}
	notice := &anthropic.Notice{
		Kind:     anthropic.NoticeKindOverageWarning,
		Text:     "notice text",
		ResetsAt: time.Unix(1, 0),
	}
	unclaimCalled := false
	encoded, ok := noticeSentinelChunkForTest()
	if !ok {
		t.Fatalf("could not build sentinel")
	}
	updated, injected := NoticeForResponseHeaders(resp, notice, func(kind string, resetsAt time.Time) {
		unclaimCalled = true
	}, json.Marshal)
	if injected {
		var got string
		if err := json.Unmarshal(updated.Choices[0].Message.Content, &got); err != nil {
			t.Fatalf("unmarshal message content: %v", err)
		}
		if got != encoded {
			t.Fatalf("expected %q, got %q", encoded, got)
		}
	}
	if !injected {
		t.Fatalf("expected notice to be injected")
	}
	if unclaimCalled {
		t.Fatalf("did not expect unclaim on success")
	}
}

func TestNoticeForResponseHeadersInvalidMessageContentUnclaims(t *testing.T) {
	t.Parallel()
	resp := ChatResponse{
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Content: json.RawMessage(`{bad`),
			},
		}},
	}
	unclaimCalled := false
	updated, injected := NoticeForResponseHeaders(resp, &anthropic.Notice{Kind: "x", Text: "note", ResetsAt: time.Unix(1, 0)}, func(kind string, resetsAt time.Time) {
		unclaimCalled = true
	}, json.Marshal)
	if injected {
		t.Fatalf("did not expect notice injection")
	}
	if updated.Choices[0].Message.Content == nil {
		t.Fatalf("expected original content preserved")
	}
	if !unclaimCalled {
		t.Fatalf("expected unclaim on invalid content")
	}
}

func TestNoticeForResponseHeadersNilNotice(t *testing.T) {
	t.Parallel()
	resp := ChatResponse{
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Content: json.RawMessage(`"hello"`),
			},
		}},
	}
	updated, injected := NoticeForResponseHeaders(resp, nil, func(kind string, resetsAt time.Time) {
		t.Fatalf("should not unclaim when notice nil")
	}, json.Marshal)
	if injected {
		t.Fatalf("expected no injection")
	}
	if string(updated.Choices[0].Message.Content) != `"hello"` {
		t.Fatalf("content changed unexpectedly: %q", updated.Choices[0].Message.Content)
	}
}

func TestNoticeForStreamHeadersInjectsAndSkipsOnError(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-status", "rejected")
	h.Set("anthropic-ratelimit-unified-overage-status", "allowed_warning")
	h.Set("anthropic-ratelimit-unified-overage-reset", "1735689600")
	h.Set("anthropic-ratelimit-unified-representative-claim", "messages")
	calls := 0
	_, err := NoticeForStreamHeaders(
		"req-1",
		"model",
		h,
		true,
		func(chunk adapteropenai.StreamChunk) error {
			calls++
			return nil
		},
		func(kind string, resetsAt time.Time) bool { return true },
		func(kind string, resetsAt time.Time) {},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 emitted notice chunk, got %d", calls)
	}

	calls = 0
	unclaimed := false
	_, err = NoticeForStreamHeaders(
		"req-1",
		"model",
		h,
		true,
		func(chunk adapteropenai.StreamChunk) error {
			calls++
			return errors.New("emit failed")
		},
		func(kind string, resetsAt time.Time) bool { return true },
		func(kind string, resetsAt time.Time) { unclaimed = true },
	)
	if err == nil {
		t.Fatalf("expected emit error to be returned")
	}
	if calls != 1 {
		t.Fatalf("expected emit to be attempted once, got %d", calls)
	}
	if !unclaimed {
		t.Fatalf("expected unclaim when emit fails")
	}
}

func TestEscalateOrWrite(t *testing.T) {
	t.Parallel()
	escalateErr := errors.New("upstream")
	wasWritten := false
	got := EscalateOrWrite(
		escalateErr,
		true,
		func(status int, code, msg string) error {
			wasWritten = true
			return nil
		},
		500,
		"code",
		"msg",
	)
	if got != escalateErr {
		t.Fatalf("expected escalate return, got %v", got)
	}
	if wasWritten {
		t.Fatalf("did not expect write path on escalate")
	}

	var writeStatus int
	var writeCode, writeMsg string
	wasWritten = false
	err := EscalateOrWrite(
		errors.New("upstream"),
		false,
		func(status int, code, msg string) error {
			wasWritten = true
			writeStatus = status
			writeCode = code
			writeMsg = msg
			return nil
		},
		400,
		"bad_request",
		"bad message",
	)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !wasWritten {
		t.Fatalf("expected write path")
	}
	if writeStatus != 400 || writeCode != "bad_request" || writeMsg != "bad message" {
		t.Fatalf("unexpected write args: %d %q %q", writeStatus, writeCode, writeMsg)
	}
}

func noticeSentinelChunkForTest() (string, bool) {
	text := noticeSentinelText("notice text")
	if text == "" {
		return "", false
	}
	return text + "hello", true
}
