package codex

import (
    "context"
    "errors"
    "io"
    "log/slog"
    "testing"
    "time"

    adaptermodel "goodkind.io/clyde/internal/adapter/model"
    adapteropenai "goodkind.io/clyde/internal/adapter/openai"
    adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

type fakeSelector struct {
    appFallbackEnabled bool
    log               *slog.Logger
    terminals         []adapterruntime.RequestEvent
}

func (f *fakeSelector) AppFallbackEnabled() bool { return f.appFallbackEnabled }
func (f *fakeSelector) Log() *slog.Logger {
    if f.log == nil {
        f.log = slog.New(slog.NewTextHandler(io.Discard, nil))
    }
    return f.log
}
func (f *fakeSelector) LogTerminal(_ context.Context, ev adapterruntime.RequestEvent) {
    f.terminals = append(f.terminals, ev)
}

func TestResolveTransportSelectionKeepsDirectOnHealthyPath(t *testing.T) {
    sel := &fakeSelector{appFallbackEnabled: true}
    path, res, managed, err := resolveTransportSelection(
        sel,
        context.Background(),
        adapteropenai.ChatRequest{
            Messages: []adapteropenai.ChatMessage{{Role: "user", Content: []byte(`"hello"`)}},
        },
        adaptermodel.ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4", Backend: adaptermodel.BackendCodex},
        "req-1",
        time.Now(),
        nil,
        RunResult{FinishReason: "stop"},
        nil,
        false,
        func() (any, bool, error) { t.Fatal("fallback should not run"); return nil, false, nil },
    )
    if err != nil {
        t.Fatalf("err=%v", err)
    }
    if path != "direct" {
        t.Fatalf("path=%q want direct", path)
    }
    if got, ok := res.(RunResult); !ok || got.FinishReason != "stop" {
        t.Fatalf("res=%v want RunResult{FinishReason: stop}", res)
    }
    if managed {
        t.Fatalf("managed=%v want false", managed)
    }
}

func TestResolveTransportSelectionFallsBackToApp(t *testing.T) {
    sel := &fakeSelector{appFallbackEnabled: true}
    path, res, managed, err := resolveTransportSelection(
        sel,
        context.Background(),
        adapteropenai.ChatRequest{
            Tools: []adapteropenai.Tool{{Type: "function", Function: adapteropenai.ToolFunctionSchema{Name: "WriteFile", Parameters: []byte(`{"type":"object"}`)}}},
            Messages: []adapteropenai.ChatMessage{{Role: "user", Content: []byte(`"Please write your answer to a markdown file on disk"`)}},
        },
        adaptermodel.ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4", Backend: adaptermodel.BackendCodex},
        "req-1",
        time.Now(),
        nil,
        RunResult{FinishReason: "stop"},
        nil,
        true,
        func() (any, bool, error) { return "app-result", true, nil },
    )
    if err != nil {
        t.Fatalf("err=%v", err)
    }
    if path != "app" {
        t.Fatalf("path=%q want app", path)
    }
    if res != "app-result" {
        t.Fatalf("res=%v want app-result", res)
    }
    if !managed {
        t.Fatalf("managed=%v want true", managed)
    }
}

func TestResolveTransportSelectionReturnsFallbackError(t *testing.T) {
    sel := &fakeSelector{appFallbackEnabled: true}
    fallbackErr := errors.New("app failed")
    path, _, _, err := resolveTransportSelection(
        sel,
        context.Background(),
        adapteropenai.ChatRequest{
            Tools: []adapteropenai.Tool{{Type: "function", Function: adapteropenai.ToolFunctionSchema{Name: "WriteFile", Parameters: []byte(`{"type":"object"}`)}}},
            Messages: []adapteropenai.ChatMessage{{Role: "user", Content: []byte(`"Please write your answer to a markdown file on disk"`)}},
        },
        adaptermodel.ResolvedModel{Alias: "gpt-5.4", ClaudeModel: "gpt-5.4", Backend: adaptermodel.BackendCodex},
        "req-1",
        time.Now(),
        nil,
        RunResult{FinishReason: "stop"},
        nil,
        false,
        func() (any, bool, error) { return nil, false, fallbackErr },
    )
    if !errors.Is(err, fallbackErr) {
        t.Fatalf("err=%v want fallback err", err)
    }
    if path != "app" {
        t.Fatalf("path=%q want app", path)
    }
}
