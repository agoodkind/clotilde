package adapter

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	"goodkind.io/clyde/internal/config"
)

func TestHandleModelsIncludesLegacyAndOpenAIContextFields(t *testing.T) {
	cfg := modelMatrixConfig()
	srv, err := New(cfg, config.LoggingConfig{
		Body: config.LoggingBody{Mode: "off"},
	}, Deps{
		ScratchDir: func() string { return t.TempDir() },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	resp := httptest.NewRecorder()
	srv.mux.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	const alias = "clyde-opus-4-7-medium-1m-thinking-enabled"
	for _, model := range payload.Data {
		if model["id"] != alias {
			continue
		}
		for _, key := range []string{"context", "context_window", "context_length", "max_model_len"} {
			if got := int(model[key].(float64)); got != 1_000_000 {
				t.Fatalf("%s=%d want 1000000 in %v", key, got, model)
			}
		}
		return
	}
	t.Fatalf("model %q not found", alias)
}

func TestModelEntryFromResolvedIsBackendNeutral(t *testing.T) {
	entry := modelEntryFromResolved(ResolvedModel{
		Alias:       "clyde-gpt-5.4-1m-high",
		Backend:     BackendCodex,
		ClaudeModel: "gpt-5.4",
		Context:     1_000_000,
	})

	if entry.ID != "clyde-gpt-5.4-1m-high" || entry.Backend != BackendCodex {
		t.Fatalf("entry identity = %+v", entry)
	}
	if entry.Context != 1_000_000 || entry.ContextWindow != 1_000_000 || entry.ContextLength != 1_000_000 || entry.MaxModelLen != 1_000_000 {
		t.Fatalf("context fields = %+v", entry)
	}
}

func TestCodexCapabilityOverlayAppliesTransportAwareContextTruth(t *testing.T) {
	entry := modelEntryFromResolved(ResolvedModel{
		Alias:       "clyde-gpt-5.4-1m-high",
		Backend:     BackendCodex,
		ClaudeModel: "gpt-5.4",
		Context:     1_000_000,
	})
	entry = adaptercodex.ApplyCapabilityReport(entry, adaptercodex.CapabilityReportForModel(ResolvedModel{
		Alias:       "clyde-gpt-5.4-1m-high",
		Backend:     BackendCodex,
		ClaudeModel: "gpt-5.4",
		Context:     1_000_000,
	}, adaptercodex.CapabilityMode{WebsocketEnabled: false}))

	for _, got := range []int{entry.Context, entry.ContextWindow, entry.ContextLength, entry.MaxModelLen} {
		if got != 272000 {
			t.Fatalf("observed context fields = %+v", entry)
		}
	}
	for _, got := range []int{entry.ContextTokenLimit, entry.ContextTokenLimitCamel, entry.ContextTokenLimitForMaxMode, entry.ContextTokenLimitForMaxModeCamel} {
		if got != 244800 {
			t.Fatalf("effective safe fields = %+v", entry)
		}
	}
}
