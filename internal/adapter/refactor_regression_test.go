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
	sw.WriteSSEHeaders()
	sw.WriteSSEHeaders()
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
}
