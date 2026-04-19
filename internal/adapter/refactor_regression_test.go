package adapter

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSSEWriterWriteHeadersIdempotent(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newSSEWriter(rec)
	if err != nil {
		t.Fatal(err)
	}
	sw.writeSSEHeaders()
	sw.writeSSEHeaders()
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
}

func TestStructuredOutputFirstPassJSONObject(t *testing.T) {
	coerced, ok := structuredOutputFirstPass(`{"a":1}`)
	if !ok {
		t.Fatal("expected JSON ok")
	}
	if coerced == "" {
		t.Fatal("empty coerced")
	}
}

func TestShuntStructuredOutputNeedsRetry(t *testing.T) {
	if !shuntStructuredOutputNeedsRetry("json_schema", true, false) {
		t.Fatal("expected retry")
	}
	if shuntStructuredOutputNeedsRetry("", true, false) {
		t.Fatal("no mode")
	}
	if shuntStructuredOutputNeedsRetry("json_schema", false, false) {
		t.Fatal("not ok status")
	}
	if shuntStructuredOutputNeedsRetry("json_schema", true, true) {
		t.Fatal("coercion ok")
	}
}
