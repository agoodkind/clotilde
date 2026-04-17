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
	// Newer Claude Code format: user content is an array of blocks
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
	// User entries with only tool_result content should be skipped
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
	// User entry with both text and tool_result blocks: text should be extracted
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

// TestStripContent_EmptyAfterThinking covers the regression where stripping
// the only block (a thinking block) left `"content":null` in the transcript,
// which crashed Claude Code on resume with:
//
//	ERROR null is not an object (evaluating 'H.message.content.length')
//
// Expected behavior: content becomes `[]` (empty array), never `null`.
func TestStripContent_EmptyAfterThinking(t *testing.T) {
	line := `{"type":"assistant","uuid":"a1","parentUuid":"u1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"some thoughts"}]}}`
	lines := []string{line}

	result, _ := StripContent(lines, []int{0}, CompactOptions{StripThinking: true})
	if len(result) != 1 {
		t.Fatalf("expected 1 result line, got %d", len(result))
	}
	if strings.Contains(result[0], `"content":null`) {
		t.Errorf("content became null after strip, want []:\n%s", result[0])
	}
	if !strings.Contains(result[0], `"content":[]`) {
		t.Errorf("expected empty array content, got:\n%s", result[0])
	}
}

// TestStripContent_ThinkingIndependentFromToolResults verifies that
// --strip-tool-results no longer implicitly strips thinking blocks, and
// that --strip-thinking leaves tool_result blocks untouched.
func TestStripContent_ThinkingIndependentFromToolResults(t *testing.T) {
	// Assistant entry with both a thinking block and a tool_use block.
	assistant := `{"type":"assistant","uuid":"a1","timestamp":"2026-04-10T10:00:00Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"pondering"},{"type":"tool_use","id":"t1","name":"Read","input":{"path":"/x"}}]}}`
	// User entry with a tool_result.
	user := `{"type":"user","uuid":"u1","timestamp":"2026-04-10T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file contents here"}]}}`
	lines := []string{assistant, user}

	// Only --strip-tool-results: thinking must survive, tool_result must be stubbed.
	only := []int{0, 1}
	r1, _ := StripContent(lines, only, CompactOptions{StripToolResults: true})
	if !strings.Contains(r1[0], `"type":"thinking"`) {
		t.Errorf("thinking was stripped by --strip-tool-results alone:\n%s", r1[0])
	}
	if !strings.Contains(r1[1], "result stripped during compact") {
		t.Errorf("tool_result was not stubbed by --strip-tool-results:\n%s", r1[1])
	}

	// Only --strip-thinking: thinking must be gone, tool_result must be intact.
	r2, _ := StripContent(lines, only, CompactOptions{StripThinking: true})
	if strings.Contains(r2[0], `"type":"thinking"`) {
		t.Errorf("thinking survived --strip-thinking:\n%s", r2[0])
	}
	if !strings.Contains(r2[1], "file contents here") {
		t.Errorf("tool_result was touched by --strip-thinking alone:\n%s", r2[1])
	}
}
