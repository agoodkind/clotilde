package render

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestEventRendererSuppressesArgumentOnlyToolDeltaLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewEventRenderer("req", "alias", "codex", log)

	chunks := r.HandleEvent(Event{
		Kind: EventToolCallDelta,
		ToolCalls: []adapteropenai.ToolCall{{
			Index: 0,
			Type:  "function",
			Function: adapteropenai.ToolCallFunction{
				Arguments: strings.Repeat("x", 128),
			},
		}},
	})
	if len(chunks) != 1 {
		t.Fatalf("chunks=%d want 1", len(chunks))
	}
	if strings.TrimSpace(buf.String()) != "" {
		t.Fatalf("argument-only delta should not log, got %s", buf.String())
	}
	r.Flush()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("summary log lines=%d want 1: %s", len(lines), buf.String())
	}
	var evt map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &evt); err != nil {
		t.Fatalf("unmarshal summary log: %v", err)
	}
	if evt["msg"] != "adapter.event.delta_summary" {
		t.Fatalf("msg=%v", evt["msg"])
	}
	if evt["event_kind"] != string(EventToolCallDelta) {
		t.Fatalf("event_kind=%v", evt["event_kind"])
	}
	if evt["delta_count"].(float64) != 1 || evt["tool_call_arg_chars"].(float64) != 128 {
		t.Fatalf("summary=%v", evt)
	}
}

func TestEventRendererLogsToolCallIdentitySummary(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewEventRenderer("req", "alias", "codex", log)

	_ = r.HandleEvent(Event{
		Kind: EventToolCallDelta,
		ToolCalls: []adapteropenai.ToolCall{{
			Index: 0,
			ID:    "call_1",
			Type:  "function",
			Function: adapteropenai.ToolCallFunction{
				Name: "ApplyPatch",
			},
		}},
	})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines=%d want 2: %s", len(lines), buf.String())
	}
	var evt map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &evt); err != nil {
		t.Fatalf("unmarshal log: %v", err)
	}
	if evt["msg"] != "adapter.event.normalized" {
		t.Fatalf("msg=%v", evt["msg"])
	}
	names, _ := evt["tool_call_names"].([]any)
	if len(names) != 1 || names[0] != "ApplyPatch" {
		t.Fatalf("tool_call_names=%v", evt["tool_call_names"])
	}
}
