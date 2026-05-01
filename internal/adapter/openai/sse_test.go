package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmitStreamErrorWritesNativeEnvelope locks in the OpenAI-shaped
// SSE error frame: `data: {"error":{"message":...,"type":...,"code":...}}\n\n`.
// Cursor and OpenAI SDK consumers consume this shape directly; the
// adapter must not synthesize it as an assistant message.
func TestEmitStreamErrorWritesNativeEnvelope(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	sw, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}

	if err := sw.EmitStreamError(ErrorBody{
		Message: "anthropic 429: limit exceeded",
		Type:    "rate_limit_error",
		Code:    "rate_limit_exceeded",
	}); err != nil {
		t.Fatalf("EmitStreamError: %v", err)
	}

	body := rr.Body.String()
	if !strings.HasPrefix(body, "data: {") {
		t.Fatalf("envelope must start with `data: {`, got %q", body)
	}
	if !strings.Contains(body, `"error":{`) {
		t.Fatalf("envelope must contain native OpenAI error key, got %q", body)
	}
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Fatalf("envelope must preserve type, got %q", body)
	}
	if !strings.Contains(body, `"code":"rate_limit_exceeded"`) {
		t.Fatalf("envelope must preserve code, got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("envelope must end with double newline, got %q", body)
	}
	if rr.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type must be text/event-stream, got %q", rr.Header().Get("Content-Type"))
	}
}

func TestSSEWriterWriteHeadersIdempotent(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := NewSSEWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	sw.WriteSSEHeaders()
	sw.WriteSSEHeaders()
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
}
