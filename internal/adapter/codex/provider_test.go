package codex

import (
	"context"
	"errors"
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
)

type fakeAuth struct {
	token string
	err   error
}

func (f fakeAuth) Token(_ context.Context) (string, error) {
	return f.token, f.err
}

type capturingWriter struct {
	events []adapterrender.Event
	flushed bool
}

func (c *capturingWriter) WriteEvent(ev adapterrender.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func (c *capturingWriter) Flush() error {
	c.flushed = true
	return nil
}

func TestProviderID(t *testing.T) {
	p := NewProvider(adapterprovider.Deps{}, ProviderOptions{})
	if got := p.ID(); got != adapterresolver.ProviderCodex {
		t.Fatalf("ID() = %v, want %v", got, adapterresolver.ProviderCodex)
	}
}

func TestProviderExecuteNilAuthReturnsAuthMissing(t *testing.T) {
	p := NewProvider(adapterprovider.Deps{}, ProviderOptions{})
	w := &capturingWriter{}
	_, err := p.Execute(context.Background(), adapterresolver.ResolvedRequest{}, w)
	if !errors.Is(err, adapterprovider.ErrAuthMissing) {
		t.Fatalf("Execute() err = %v, want ErrAuthMissing", err)
	}
}

func TestProviderExecuteEmptyTokenReturnsAuthMissing(t *testing.T) {
	p := NewProvider(adapterprovider.Deps{Auth: fakeAuth{token: "  "}}, ProviderOptions{})
	w := &capturingWriter{}
	_, err := p.Execute(context.Background(), adapterresolver.ResolvedRequest{}, w)
	if !errors.Is(err, adapterprovider.ErrAuthMissing) {
		t.Fatalf("Execute() err = %v, want ErrAuthMissing", err)
	}
}

func TestProviderExecuteAuthErrorSurfaces(t *testing.T) {
	want := errors.New("auth boom")
	p := NewProvider(adapterprovider.Deps{Auth: fakeAuth{err: want}}, ProviderOptions{})
	w := &capturingWriter{}
	_, err := p.Execute(context.Background(), adapterresolver.ResolvedRequest{}, w)
	if !errors.Is(err, want) {
		t.Fatalf("Execute() err = %v, want %v", err, want)
	}
}

func TestCodexBaseURLDefaults(t *testing.T) {
	if got := codexBaseURL(""); got != "https://chatgpt.com/backend-api/codex/responses" {
		t.Errorf("codexBaseURL(\"\") = %q, want default", got)
	}
	custom := "https://example.com/codex"
	if got := codexBaseURL(custom); got != custom {
		t.Errorf("codexBaseURL custom = %q, want %q", got, custom)
	}
}

func TestCodexWebsocketURLConversion(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "wss://chatgpt.com/backend-api/codex/responses"},
		{"https://example.com/codex", "wss://example.com/codex"},
		{"http://localhost:8080/codex", "ws://localhost:8080/codex"},
		{"weird://something", "weird://something"},
	}
	for _, tc := range cases {
		if got := codexWebsocketURL(tc.in); got != tc.want {
			t.Errorf("codexWebsocketURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestChunkPrimaryTextExtraction(t *testing.T) {
	cases := []struct {
		name string
		in   adapteropenai.StreamChunk
		want string
	}{
		{
			name: "empty chunk",
			in:   adapteropenai.StreamChunk{},
			want: "",
		},
		{
			name: "first choice carries text",
			in: adapteropenai.StreamChunk{Choices: []adapteropenai.StreamChoice{
				{Delta: adapteropenai.StreamDelta{Content: "hello"}},
			}},
			want: "hello",
		},
		{
			name: "trims whitespace-only deltas",
			in: adapteropenai.StreamChunk{Choices: []adapteropenai.StreamChoice{
				{Delta: adapteropenai.StreamDelta{Content: "   "}},
				{Delta: adapteropenai.StreamDelta{Content: "world"}},
			}},
			want: "world",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chunkPrimaryText(tc.in); got != tc.want {
				t.Errorf("chunkPrimaryText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChunkToEventBridgeEmitsTextDelta(t *testing.T) {
	w := &capturingWriter{}
	emit := chunkToEventBridge(w)
	chunk := adapteropenai.StreamChunk{Choices: []adapteropenai.StreamChoice{
		{Delta: adapteropenai.StreamDelta{Content: "hi"}},
	}}
	if err := emit(chunk); err != nil {
		t.Fatalf("emit() err = %v", err)
	}
	if len(w.events) != 1 {
		t.Fatalf("got %d events, want 1", len(w.events))
	}
	if w.events[0].Kind != adapterrender.EventAssistantTextDelta {
		t.Errorf("event kind = %v, want EventAssistantTextDelta", w.events[0].Kind)
	}
	if w.events[0].Text != "hi" {
		t.Errorf("event text = %q, want hi", w.events[0].Text)
	}
}

func TestChunkToEventBridgeSkipsEmptyText(t *testing.T) {
	w := &capturingWriter{}
	emit := chunkToEventBridge(w)
	if err := emit(adapteropenai.StreamChunk{}); err != nil {
		t.Fatalf("emit() err = %v", err)
	}
	if len(w.events) != 0 {
		t.Errorf("got %d events, want 0 (empty chunks are dropped)", len(w.events))
	}
}

func TestResolvedModelFromRequestPopulatesCodexFields(t *testing.T) {
	req := adapterresolver.ResolvedRequest{
		Provider: adapterresolver.ProviderCodex,
		Family:   "gpt-5",
		Model:    "gpt-5.3-codex",
		Effort:   adapterresolver.EffortHigh,
		ContextBudget: adapterresolver.ContextBudget{
			InputTokens:  200000,
			OutputTokens: 16384,
		},
	}
	rm := resolvedModelFromRequest(req)
	if rm.Alias != "gpt-5.3-codex" {
		t.Errorf("Alias = %q", rm.Alias)
	}
	if rm.ClaudeModel != "gpt-5.3-codex" {
		t.Errorf("ClaudeModel = %q", rm.ClaudeModel)
	}
	if rm.Context != 200000 {
		t.Errorf("Context = %d", rm.Context)
	}
	if rm.MaxOutputTokens != 16384 {
		t.Errorf("MaxOutputTokens = %d", rm.MaxOutputTokens)
	}
	if rm.Effort != "high" {
		t.Errorf("Effort = %q", rm.Effort)
	}
	if rm.FamilySlug != "gpt-5" {
		t.Errorf("FamilySlug = %q", rm.FamilySlug)
	}
}
