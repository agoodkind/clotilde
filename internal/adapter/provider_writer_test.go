package adapter

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
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

func TestCodexProviderErrorResponseMapsWrappedUnsupportedModelError(t *testing.T) {
	t.Parallel()

	wrapped := errors.Join(errors.New("codex websocket warmup failed"), &adaptercodex.UnsupportedModelError{
		Message: "The '5.5' model is not supported when using Codex with a ChatGPT account.",
	})
	status, body := codexProviderErrorResponse(wrapped)

	if status != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", status, http.StatusBadRequest)
	}
	if body.Type != "invalid_request_error" || body.Code != "model_not_supported" || body.Param != "model" {
		t.Fatalf("body=%+v", body)
	}
	if body.Message != "The '5.5' model is not supported when using Codex with a ChatGPT account." {
		t.Fatalf("message=%q", body.Message)
	}
}

func TestCodexProviderErrorResponseMapsGenericProviderError(t *testing.T) {
	t.Parallel()

	status, body := codexProviderErrorResponse(errors.New("codex websocket read failed"))

	if status != http.StatusBadGateway {
		t.Fatalf("status=%d want %d", status, http.StatusBadGateway)
	}
	if body.Type != "server_error" || body.Code != "upstream_failed" || body.Param != "" {
		t.Fatalf("body=%+v", body)
	}
	if body.Message != "codex websocket read failed" {
		t.Fatalf("message=%q", body.Message)
	}
}

func TestAdapterErrUpstreamFailedUsesOpenAICompatibleServerError(t *testing.T) {
	t.Parallel()

	body := adapterErrUpstreamFailed("codex", "codex websocket read failed", errors.New("boom")).openAIErrorBody()

	if body.Type != "server_error" || body.Code != "upstream_failed" || body.Param != "" {
		t.Fatalf("body=%+v", body)
	}
	if body.Message != "codex websocket read failed" {
		t.Fatalf("message=%q", body.Message)
	}
}

func TestProviderStreamWriterWritesMappedErrorEnvelope(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sse, err := adapteropenai.NewSSEWriter(rec)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	writer := &providerStreamWriter{sse: sse}

	err = writer.writeStreamErrorBody(ErrorBody{
		Message: "unsupported model: gpt-5.5",
		Type:    "invalid_request_error",
		Code:    "model_not_supported",
		Param:   "model",
	})
	if err != nil {
		t.Fatalf("writeStreamErrorBody: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`"error":{`,
		`"type":"invalid_request_error"`,
		`"code":"model_not_supported"`,
		`"param":"model"`,
		"data: [DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
}
