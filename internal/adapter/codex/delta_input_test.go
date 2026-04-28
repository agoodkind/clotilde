package codex

import (
	"testing"
)

func msg(role, text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": role,
		"content": []map[string]any{{
			"type": "input_text",
			"text": text,
		}},
	}
}

func TestComputeDeltaReturnsAllItemsWhenPriorEmpty(t *testing.T) {
	current := []map[string]any{msg("user", "hi"), msg("assistant", "hello")}
	got := ComputeDelta(nil, current)
	if !got.Ok {
		t.Fatalf("expected ok, got %#v", got)
	}
	if len(got.Items) != 2 {
		t.Errorf("expected 2 delta items, got %d", len(got.Items))
	}
}

func TestComputeDeltaReturnsSuffixWhenPriorIsStrictPrefix(t *testing.T) {
	prior := []map[string]any{msg("user", "hi"), msg("assistant", "hello")}
	current := []map[string]any{msg("user", "hi"), msg("assistant", "hello"), msg("user", "next question")}
	got := ComputeDelta(prior, current)
	if !got.Ok {
		t.Fatalf("expected ok, got reason %q", got.Reason)
	}
	if len(got.Items) != 1 {
		t.Errorf("expected single new item, got %d", len(got.Items))
	}
	if text := got.Items[0]["content"].([]map[string]any)[0]["text"]; text != "next question" {
		t.Errorf("expected delta text=next question, got %v", text)
	}
}

func TestComputeDeltaNoExtensionWhenEqual(t *testing.T) {
	prior := []map[string]any{msg("user", "hi")}
	current := []map[string]any{msg("user", "hi")}
	got := ComputeDelta(prior, current)
	if got.Ok {
		t.Errorf("equal lists should not produce delta")
	}
	if got.Reason != "no_extension" {
		t.Errorf("expected reason=no_extension, got %q", got.Reason)
	}
}

func TestComputeDeltaBaselineDivergenceOnRewrite(t *testing.T) {
	prior := []map[string]any{msg("user", "hi"), msg("assistant", "hello")}
	current := []map[string]any{msg("user", "different"), msg("assistant", "hello")}
	got := ComputeDelta(prior, current)
	if got.Ok {
		t.Errorf("rewrite should not produce delta")
	}
	if got.Reason != "baseline_divergence" {
		t.Errorf("expected baseline_divergence, got %q", got.Reason)
	}
}

func TestComputeDeltaBaselineDivergenceWhenCurrentShorter(t *testing.T) {
	prior := []map[string]any{msg("user", "a"), msg("assistant", "b"), msg("user", "c")}
	current := []map[string]any{msg("user", "a")}
	got := ComputeDelta(prior, current)
	if got.Ok {
		t.Errorf("shorter current should not produce delta")
	}
	if got.Reason != "baseline_divergence" {
		t.Errorf("expected baseline_divergence, got %q", got.Reason)
	}
}

func TestComputeDeltaEmptyEverything(t *testing.T) {
	got := ComputeDelta(nil, nil)
	if got.Ok {
		t.Errorf("empty/empty should not be ok")
	}
	if got.Reason != "empty_input" {
		t.Errorf("expected empty_input, got %q", got.Reason)
	}
}
