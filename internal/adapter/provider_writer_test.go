package adapter

import (
	"errors"
	"net/http"
	"testing"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
)

func TestNormalizedProviderFinishReasonPreservesToolCallTerminalState(t *testing.T) {
	t.Parallel()

	got := normalizedProviderFinishReason(adapterprovider.Result{
		FinishReason:  "stop",
		ToolCallCount: 1,
	})
	if got != "tool_calls" {
		t.Fatalf("finish_reason=%q want tool_calls", got)
	}
}

func TestCodexProviderErrorResponseMapsContextWindowError(t *testing.T) {
	t.Parallel()

	status, body := codexProviderErrorResponse(&adaptercodex.ContextWindowError{
		Message: "Your input exceeds the context window of this model. Please adjust your input and try again.",
	})

	if status != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", status, http.StatusBadRequest)
	}
	if body.Type != "invalid_request_error" || body.Code != "context_length_exceeded" || body.Param != "messages" {
		t.Fatalf("body=%+v", body)
	}
	if body.Message != "This model's maximum context length was exceeded. Please reduce the length of the messages." {
		t.Fatalf("message=%q", body.Message)
	}
}

func TestCodexProviderErrorResponseMapsWrappedContextWindowError(t *testing.T) {
	t.Parallel()

	wrapped := errors.Join(errors.New("transport failed"), &adaptercodex.ContextWindowError{
		Message: "context_length_exceeded",
	})
	status, body := codexProviderErrorResponse(wrapped)

	if status != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", status, http.StatusBadRequest)
	}
	if body.Code != "context_length_exceeded" {
		t.Fatalf("code=%q want context_length_exceeded", body.Code)
	}
}
