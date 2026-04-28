package codex

import (
	"testing"
)

// Cursor prunes intermediate reasoning items between turns. The
// strict contiguous-subsequence baseline matcher fails because the
// stored output sequence (reasoning + assistant message) does not
// survive intact. The permissive anchor path locates the last
// identity-bearing item we stored (a tool call with a call_id, or
// reasoning with an id) inside the current input and treats
// everything after as the incremental delta.

func TestDiffOutputBaselineHitsViaAnchorWhenReasoningPruned(t *testing.T) {
	stored := []map[string]any{
		{"type": "reasoning", "id": "rs_anchor_1", "summary": []map[string]any{{"type": "summary_text", "text": "thinking"}}},
		{"type": "function_call", "call_id": "call_abc", "name": "ReadFile", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_abc", "output": "ok"},
	}
	// Cursor's next-turn input includes the conversation so far PLUS
	// new user input, but Cursor pruned the reasoning item we stored
	// at the head. The strict path cannot find the contiguous
	// sequence because reasoning is missing, but the anchor path
	// finds call_id=call_abc on function_call_output.
	current := []map[string]any{
		{"type": "message", "role": "system", "content": []map[string]any{{"type": "input_text", "text": "system"}}},
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "do the thing"}}},
		{"type": "function_call", "call_id": "call_abc", "name": "ReadFile", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_abc", "output": "ok"},
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "now do the next thing"}}},
	}
	got := diffContinuationOutputBaseline(stored, current)
	if got.Reason != "" {
		t.Fatalf("expected hit via anchor, got reason=%q", got.Reason)
	}
	if len(got.IncrementalInput) != 1 {
		t.Fatalf("expected 1 incremental input item, got %d", len(got.IncrementalInput))
	}
	if got.IncrementalInput[0]["role"] != "user" {
		t.Fatalf("expected incremental[0] to be user message, got: %v", got.IncrementalInput[0])
	}
}

func TestDiffOutputBaselineAnchorRequiresIdentityMatch(t *testing.T) {
	stored := []map[string]any{
		{"type": "function_call", "call_id": "call_xyz", "name": "ReadFile", "arguments": "{}"},
	}
	// current has a function_call but with a DIFFERENT call_id; the
	// anchor cannot match.
	current := []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "hi"}}},
		{"type": "function_call", "call_id": "call_unrelated", "name": "ReadFile", "arguments": "{}"},
	}
	got := diffContinuationOutputBaseline(stored, current)
	if got.Reason != "output_item_baseline_mismatch" {
		t.Fatalf("expected output_item_baseline_mismatch, got reason=%q", got.Reason)
	}
}

func TestDiffOutputBaselineAnchorPrefersLastIdentity(t *testing.T) {
	// Both call_id values exist in current, but call_b is later.
	// The matcher should anchor on the LAST identity-bearing item
	// from stored that finds a match.
	stored := []map[string]any{
		{"type": "function_call", "call_id": "call_a", "name": "ReadFile", "arguments": "{}"},
		{"type": "function_call", "call_id": "call_b", "name": "ReadFile", "arguments": "{}"},
	}
	current := []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "hi"}}},
		{"type": "function_call", "call_id": "call_a", "name": "ReadFile", "arguments": "{}"},
		{"type": "function_call", "call_id": "call_b", "name": "ReadFile", "arguments": "{}"},
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "next"}}},
	}
	got := diffContinuationOutputBaseline(stored, current)
	if got.Reason != "" {
		t.Fatalf("expected hit, got reason=%q", got.Reason)
	}
	if len(got.IncrementalInput) != 1 {
		t.Fatalf("expected 1 incremental, got %d", len(got.IncrementalInput))
	}
	if got.IncrementalInput[0]["role"] != "user" {
		t.Fatalf("expected user role on tail, got: %v", got.IncrementalInput[0])
	}
}

func TestDiffOutputBaselineFallsBackWhenNoIdentityAtAll(t *testing.T) {
	// stored has only message items (no Identity). Anchor path
	// cannot help. Strict path is the only option.
	stored := []map[string]any{
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "hello"}}},
	}
	current := []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "hi"}}},
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "hello"}}},
		{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "again"}}},
	}
	got := diffContinuationOutputBaseline(stored, current)
	if got.Reason != "" {
		t.Fatalf("strict path should hit, got reason=%q", got.Reason)
	}
	if len(got.IncrementalInput) != 1 {
		t.Fatalf("expected 1 incremental, got %d", len(got.IncrementalInput))
	}
}
