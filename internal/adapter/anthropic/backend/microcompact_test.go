package anthropicbackend

import (
	"strings"
	"testing"

	"goodkind.io/clyde/internal/adapter/anthropic"
)

func TestApplyMicrocompactClearsOlderToolResults(t *testing.T) {
	msgs := buildToolConvo(5) // 5 compactable tool_use/result pairs
	cleared, bytes := ApplyMicrocompact(msgs, 2)
	if cleared != 3 {
		t.Fatalf("want 3 cleared, got %d", cleared)
	}
	if bytes == 0 {
		t.Fatalf("bytes_saved should be nonzero when anything was cleared")
	}

	// Oldest three tool_result blocks now carry the sentinel.
	for i := range 3 {
		got := findToolResult(msgs, idForIndex(i)).Content
		if got != MicrocompactClearedMessage {
			t.Fatalf("result %d not cleared: %q", i, got)
		}
	}
	// Last two tool_result blocks stay verbatim.
	for i := 3; i < 5; i++ {
		got := findToolResult(msgs, idForIndex(i)).Content
		if got == MicrocompactClearedMessage {
			t.Fatalf("result %d should be kept, was cleared", i)
		}
	}
}

func TestApplyMicrocompactNoOpWhenUnderThreshold(t *testing.T) {
	msgs := buildToolConvo(3)
	cleared, _ := ApplyMicrocompact(msgs, 10)
	if cleared != 0 {
		t.Fatalf("want 0 cleared when history <= keepRecent, got %d", cleared)
	}
}

func TestApplyMicrocompactDefaultKeepRecent(t *testing.T) {
	msgs := buildToolConvo(20) // above default of 15
	cleared, _ := ApplyMicrocompact(msgs, 0)
	if cleared != 20-DefaultMicrocompactKeepRecent {
		t.Fatalf("want %d cleared at default keep, got %d", 20-DefaultMicrocompactKeepRecent, cleared)
	}
}

func TestApplyMicrocompactIdempotent(t *testing.T) {
	msgs := buildToolConvo(5)
	_, _ = ApplyMicrocompact(msgs, 2)
	cleared, bytes := ApplyMicrocompact(msgs, 2)
	if cleared != 0 || bytes != 0 {
		t.Fatalf("second pass should be a no-op, got cleared=%d bytes=%d", cleared, bytes)
	}
}

func TestApplyMicrocompactSkipsNonCompactableTools(t *testing.T) {
	msgs := []anthropic.Message{
		{Role: "assistant", Content: []anthropic.ContentBlock{
			{Type: "tool_use", ID: "a", Name: "NotCompactable"},
		}},
		{Role: "user", Content: []anthropic.ContentBlock{
			{Type: "tool_result", ToolUseID: "a", Content: "huge output"},
		}},
		{Role: "assistant", Content: []anthropic.ContentBlock{
			{Type: "tool_use", ID: "b", Name: "Read"},
		}},
		{Role: "user", Content: []anthropic.ContentBlock{
			{Type: "tool_result", ToolUseID: "b", Content: "file contents"},
		}},
	}
	cleared, _ := ApplyMicrocompact(msgs, 0) // keepRecent = default 15
	if cleared != 0 {
		t.Fatalf("nothing to clear at default keepRecent, got %d", cleared)
	}
	cleared, _ = ApplyMicrocompact(msgs, 1) // keep only latest compactable
	// Only the Read tool is compactable; it is also the only compactable
	// call, so with keepRecent=1 nothing gets cleared.
	if cleared != 0 {
		t.Fatalf("no older compactable ids, got %d cleared", cleared)
	}
}

// buildToolConvo constructs a synthetic conversation with n
// alternating assistant(tool_use)/user(tool_result) pairs using Read
// as the tool.
func buildToolConvo(n int) []anthropic.Message {
	var out []anthropic.Message
	for i := range n {
		out = append(out, anthropic.Message{
			Role: "assistant",
			Content: []anthropic.ContentBlock{{
				Type: "tool_use",
				ID:   idForIndex(i),
				Name: "Read",
			}},
		})
		out = append(out, anthropic.Message{
			Role: "user",
			Content: []anthropic.ContentBlock{{
				Type:      "tool_result",
				ToolUseID: idForIndex(i),
				Content:   strings.Repeat("x", 1000),
			}},
		})
	}
	return out
}

func idForIndex(i int) string {
	return "toolu_" + string(rune('a'+i))
}

func findToolResult(msgs []anthropic.Message, id string) *anthropic.ContentBlock {
	for mi := range msgs {
		for bi := range msgs[mi].Content {
			b := &msgs[mi].Content[bi]
			if b.Type == "tool_result" && b.ToolUseID == id {
				return b
			}
		}
	}
	return nil
}
