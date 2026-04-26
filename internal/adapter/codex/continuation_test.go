package codex

import "testing"

func TestContinuationStoreMissesWithoutPriorResponse(t *testing.T) {
	store := NewContinuationStore()
	decision := store.Prepare(ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		PromptCacheKey: "cursor:conv-123",
		Input:          []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
	})
	if decision.Hit {
		t.Fatalf("unexpected continuation hit")
	}
	if decision.MissReason != "no_prior_response" {
		t.Fatalf("miss_reason=%q want no_prior_response", decision.MissReason)
	}
}

func TestContinuationStoreReusesPreviousResponseWithPrefixDelta(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "developer", "content": []map[string]any{{"type": "input_text", "text": "rules"}}},
			{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "hello"}}},
		},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{ResponseID: "resp-1"})

	second := first
	second.Input = append(cloneInput(first.Input), map[string]any{"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": "next"}}})
	decision := store.Prepare(second)
	if !decision.Hit {
		t.Fatalf("expected continuation hit, miss_reason=%q", decision.MissReason)
	}
	if decision.PreviousResponseID != "resp-1" {
		t.Fatalf("previous_response_id=%q want resp-1", decision.PreviousResponseID)
	}
	if len(decision.IncrementalInput) != 1 || itemRole(decision.IncrementalInput[0]) != "user" {
		t.Fatalf("incremental_input=%v", decision.IncrementalInput)
	}
}

func TestContinuationStoreUsesTailAfterAssistantForCursorReplay(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "developer"},
			{"type": "message", "role": "user", "content": "first"},
		},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{ResponseID: "resp-1"})

	second := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "user", "content": "first"},
			{"type": "message", "role": "assistant", "content": "answer"},
			{"type": "message", "role": "developer"},
			{"type": "message", "role": "user", "content": "follow up"},
		},
	}
	decision := store.Prepare(second)
	if !decision.Hit {
		t.Fatalf("expected continuation hit, miss_reason=%q", decision.MissReason)
	}
	if len(decision.IncrementalInput) != 2 {
		t.Fatalf("incremental_input=%v want context plus follow-up", decision.IncrementalInput)
	}
	if itemRole(decision.IncrementalInput[1]) != "user" {
		t.Fatalf("incremental_input=%v", decision.IncrementalInput)
	}
}

func TestContinuationStoreInvalidatesOnConfigMismatch(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input:          []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{ResponseID: "resp-1"})

	second := first
	second.Model = "gpt-5.5"
	second.Input = append(cloneInput(first.Input), map[string]any{"type": "message", "role": "user", "content": "next"})
	decision := store.Prepare(second)
	if decision.Hit {
		t.Fatalf("unexpected continuation hit")
	}
	if decision.MissReason != "fingerprint_mismatch" {
		t.Fatalf("miss_reason=%q want fingerprint_mismatch", decision.MissReason)
	}
}

func TestContinuationStoreForgetsOnFailure(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		PromptCacheKey: "cursor:conv-123",
		Input:          []map[string]any{{"type": "message", "role": "user", "content": "hello"}},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{ResponseID: "resp-1"})
	store.Forget("cursor:conv-123")

	decision := store.Prepare(first)
	if decision.MissReason != "no_prior_response" {
		t.Fatalf("miss_reason=%q want no_prior_response", decision.MissReason)
	}
}
