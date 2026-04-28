package resolver

import (
	"errors"
	"testing"

	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type stubRegistry struct {
	view ResolvedModelView
	err  error
}

func (s stubRegistry) Resolve(alias, reqEffort string) (ResolvedModelView, error) {
	if s.err != nil {
		return ResolvedModelView{}, s.err
	}
	return s.view, nil
}

func TestResolveNilRegistry(t *testing.T) {
	_, err := Resolve(adaptercursor.Request{}, nil)
	if err == nil {
		t.Fatal("expected error for nil registry, got nil")
	}
}

func TestResolveSurfacesRegistryError(t *testing.T) {
	want := errors.New("registry boom")
	_, err := Resolve(adaptercursor.Request{}, stubRegistry{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("expected error %v, got %v", want, err)
	}
}

func TestResolveRejectsUnsupportedProvider(t *testing.T) {
	_, err := Resolve(adaptercursor.Request{}, stubRegistry{view: ResolvedModelView{
		Provider: ProviderUnknown,
		Family:   "claude",
		Model:    "claude-3-5-sonnet",
	}})
	if !errors.Is(err, ErrUnresolvedProvider) {
		t.Fatalf("expected ErrUnresolvedProvider, got %v", err)
	}
}

func TestResolveBuildsTypedRequest(t *testing.T) {
	openaiReq := adapteropenai.ChatRequest{Model: "gpt-5.3-codex", ReasoningEffort: "high"}
	cursorReq := adaptercursor.Request{OpenAI: openaiReq, ConversationID: "conv-1"}
	view := ResolvedModelView{
		Provider:        ProviderCodex,
		Family:          "gpt-5",
		Model:           "gpt-5.3-codex",
		Effort:          EffortHigh,
		Context:         200000,
		MaxOutputTokens: 16384,
	}
	got, err := Resolve(cursorReq, stubRegistry{view: view})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != ProviderCodex {
		t.Errorf("Provider = %v, want %v", got.Provider, ProviderCodex)
	}
	if got.Family != "gpt-5" {
		t.Errorf("Family = %q, want gpt-5", got.Family)
	}
	if got.Model != "gpt-5.3-codex" {
		t.Errorf("Model = %q, want gpt-5.3-codex", got.Model)
	}
	if got.Effort != EffortHigh {
		t.Errorf("Effort = %v, want %v", got.Effort, EffortHigh)
	}
	if got.ContextBudget.InputTokens != 200000 {
		t.Errorf("ContextBudget.InputTokens = %d, want 200000", got.ContextBudget.InputTokens)
	}
	if got.ContextBudget.OutputTokens != 16384 {
		t.Errorf("ContextBudget.OutputTokens = %d, want 16384", got.ContextBudget.OutputTokens)
	}
	if got.Cursor.ConversationID != "conv-1" {
		t.Errorf("Cursor.ConversationID = %q, want conv-1", got.Cursor.ConversationID)
	}
	if got.OpenAI.Model != "gpt-5.3-codex" {
		t.Errorf("OpenAI.Model = %q, want gpt-5.3-codex", got.OpenAI.Model)
	}
}

func TestResolvedRequestValid(t *testing.T) {
	good := ResolvedRequest{Provider: ProviderCodex, Effort: EffortMedium, Model: "gpt-5.3-codex"}
	if !good.Valid() {
		t.Errorf("expected good ResolvedRequest to be valid")
	}
	bad := ResolvedRequest{Provider: ProviderUnknown, Effort: EffortMedium, Model: "gpt-5.3-codex"}
	if bad.Valid() {
		t.Errorf("expected bad ResolvedRequest to be invalid")
	}
	noModel := ResolvedRequest{Provider: ProviderAnthropic, Effort: EffortHigh}
	if noModel.Valid() {
		t.Errorf("expected ResolvedRequest with no Model to be invalid")
	}
}
