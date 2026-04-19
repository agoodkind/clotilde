package transcript

import (
	"strings"
	"testing"
)

func TestParseStringContentUser(t *testing.T) {
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"user","content":"hello world"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-04-10T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}`

	messages, err := Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Text != "hello world" {
		t.Errorf("user message: role=%q text=%q", messages[0].Role, messages[0].Text)
	}
	if messages[1].Role != "assistant" || messages[1].Text != "hi there" {
		t.Errorf("assistant message: role=%q text=%q", messages[1].Role, messages[1].Text)
	}
}

func TestParseArrayContentUser(t *testing.T) {
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"opened a file"},{"type":"text","text":"do something with it"}]}}`

	messages, err := Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Role != "user" {
		t.Errorf("expected role=user, got %q", messages[0].Role)
	}
	if !strings.Contains(messages[0].Text, "opened a file") {
		t.Errorf("expected text to contain 'opened a file', got %q", messages[0].Text)
	}
	if !strings.Contains(messages[0].Text, "do something with it") {
		t.Errorf("expected text to contain 'do something with it', got %q", messages[0].Text)
	}
}

func TestParseToolResultUserSkipped(t *testing.T) {
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"output"}]}}`

	messages, err := Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("expected 0 messages (tool result only), got %d", len(messages))
	}
}

func TestParseMixedUserContent(t *testing.T) {
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"here is context"},{"type":"tool_result","tool_use_id":"t1","content":"output"}]}}`

	messages, err := Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].Text != "here is context" {
		t.Errorf("expected 'here is context', got %q", messages[0].Text)
	}
}

func TestParseAssistantToolUse(t *testing.T) {
	jsonl := `{"type":"assistant","uuid":"a1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`

	messages, err := Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if !messages[0].HasTools {
		t.Error("expected HasTools=true")
	}
	if len(messages[0].Tools) != 1 || messages[0].Tools[0].Name != "Bash" {
		t.Errorf("expected tool Bash, got %v", messages[0].Tools)
	}
}

func TestParseSkipsAttachments(t *testing.T) {
	jsonl := `{"type":"attachment","uuid":"x1","timestamp":"2026-04-10T10:00:00Z","attachment":{"type":"deferred_tools"}}
{"type":"user","uuid":"u1","timestamp":"2026-04-10T10:00:01Z","message":{"role":"user","content":"real message"}}`

	messages, err := Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message (attachment skipped), got %d", len(messages))
	}
	if messages[0].Text != "real message" {
		t.Errorf("expected 'real message', got %q", messages[0].Text)
	}
}

func TestParseStripsSystemTags(t *testing.T) {
	jsonl := `{"type":"user","uuid":"u1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"user","content":"hello <system-reminder>ignore this</system-reminder> world"}}`

	messages, err := Parse(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if strings.Contains(messages[0].Text, "system-reminder") {
		t.Errorf("system tags not stripped: %q", messages[0].Text)
	}
	if !strings.Contains(messages[0].Text, "hello") || !strings.Contains(messages[0].Text, "world") {
		t.Errorf("expected 'hello ... world', got %q", messages[0].Text)
	}
}
