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

func TestContinuationStoreReusesPreviousResponseWithOutputItemBaseline(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "user", "content": "hello"},
		},
	}
	outputItem := map[string]any{
		"id":      "msg-1",
		"type":    "message",
		"role":    "assistant",
		"content": []map[string]any{{"type": "output_text", "text": "assistant output"}},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{
		ResponseID:  "resp-1",
		OutputItems: []map[string]any{outputItem},
	})

	second := first
	second.Input = append(cloneInput(first.Input), cloneMap(outputItem), map[string]any{
		"type":    "message",
		"role":    "user",
		"content": "follow up",
	})
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

func TestContinuationStoreReusesPreviousResponseWithCursorReplayedAssistantMessage(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "user", "content": "hello"},
		},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{
		ResponseID: "resp-1",
		OutputItems: []map[string]any{{
			"id":      "msg-1",
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]any{{"type": "output_text", "text": "assistant output"}},
		}},
	})

	second := first
	second.Input = append(cloneInput(first.Input),
		map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]any{{"type": "output_text", "text": "assistant output"}},
		},
		map[string]any{"type": "message", "role": "user", "content": "follow up"},
	)
	decision := store.Prepare(second)
	if !decision.Hit {
		t.Fatalf("expected continuation hit, miss_reason=%q", decision.MissReason)
	}
	if len(decision.IncrementalInput) != 1 || itemRole(decision.IncrementalInput[0]) != "user" {
		t.Fatalf("incremental_input=%v", decision.IncrementalInput)
	}
}

func TestContinuationStoreReusesPreviousResponseWithCursorReplayedToolCall(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "user", "content": "inspect"},
		},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{
		ResponseID: "resp-1",
		OutputItems: []map[string]any{{
			"id":        "fc-1",
			"type":      "function_call",
			"status":    "completed",
			"call_id":   "call_read",
			"name":      "read_file",
			"arguments": `{"path":"README.md"}`,
		}},
	})

	second := first
	second.Input = append(cloneInput(first.Input),
		map[string]any{
			"type":    "message",
			"role":    "assistant",
			"content": []map[string]any{{"type": "output_text", "text": "I will read the file."}},
		},
		map[string]any{
			"type":      "function_call",
			"call_id":   "call_read",
			"name":      "read_file",
			"arguments": `{"path":"README.md"}`,
		},
		map[string]any{
			"type":    "function_call_output",
			"call_id": "call_read",
			"output":  "contents",
		},
	)
	decision := store.Prepare(second)
	if !decision.Hit {
		t.Fatalf("expected continuation hit, miss_reason=%q", decision.MissReason)
	}
	if len(decision.IncrementalInput) != 1 || mapString(decision.IncrementalInput[0], "type") != "function_call_output" {
		t.Fatalf("incremental_input=%v", decision.IncrementalInput)
	}
}

func TestContinuationStoreAnchorsOnOutputItemsWhenCursorPreambleChanges(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "user", "content": "inspect"},
		},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{
		ResponseID: "resp-1",
		OutputItems: []map[string]any{{
			"id":        "fc-1",
			"type":      "function_call",
			"status":    "completed",
			"call_id":   "call_pwd",
			"name":      "shell_command",
			"arguments": `{"command":"pwd","workdir":"/repo"}`,
		}},
	})

	second := first
	second.Input = []map[string]any{
		{"type": "message", "role": "developer", "content": "reshaped cursor preamble"},
		{"type": "message", "role": "user", "content": "inspect"},
		{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "I will inspect."}}},
		{
			"type":      "function_call",
			"call_id":   "call_pwd",
			"name":      "shell_command",
			"arguments": `{"workdir":"/repo","command":"pwd"}`,
		},
		{
			"type":    "function_call_output",
			"call_id": "call_pwd",
			"output":  "repo",
		},
	}
	decision := store.Prepare(second)
	if !decision.Hit {
		t.Fatalf("expected continuation hit, miss_reason=%q", decision.MissReason)
	}
	if len(decision.IncrementalInput) != 1 || mapString(decision.IncrementalInput[0], "type") != "function_call_output" {
		t.Fatalf("incremental_input=%v", decision.IncrementalInput)
	}
}

func TestContinuationStoreAnchorsToolCallsByStableCallID(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "user", "content": "inspect"},
		},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{
		ResponseID: "resp-1",
		OutputItems: []map[string]any{{
			"id":        "fc-1",
			"type":      "function_call",
			"status":    "completed",
			"call_id":   "call_pwd",
			"name":      "shell_command",
			"arguments": `{"command":"pwd","workdir":"/repo"}`,
		}},
	})

	second := first
	second.Input = []map[string]any{
		{"type": "message", "role": "developer", "content": "reshaped cursor preamble"},
		{"type": "message", "role": "user", "content": "inspect"},
		{
			"type":      "function_call",
			"call_id":   "call_pwd",
			"name":      "Shell",
			"arguments": `{"working_directory":"/repo","command":"pwd","block_until_ms":1000}`,
		},
		{"type": "message", "role": "user", "content": "next"},
	}
	decision := store.Prepare(second)
	if !decision.Hit {
		t.Fatalf("expected continuation hit, miss_reason=%q", decision.MissReason)
	}
	if len(decision.IncrementalInput) != 1 || itemRole(decision.IncrementalInput[0]) != "user" {
		t.Fatalf("incremental_input=%v", decision.IncrementalInput)
	}
}

func TestContinuationStoreAnchorsNativeLocalShellCallsByStableCallID(t *testing.T) {
	store := NewContinuationStore()
	first := ResponseCreateWsRequest{
		Type:           "response.create",
		Model:          "gpt-5.4",
		Instructions:   "base",
		PromptCacheKey: "cursor:conv-123",
		Input: []map[string]any{
			{"type": "message", "role": "user", "content": "inspect"},
		},
	}
	store.Complete(ContinuationDecision{}, first, RunResult{
		ResponseID: "resp-1",
		OutputItems: []map[string]any{{
			"id":      "ls-1",
			"type":    "local_shell_call",
			"status":  "completed",
			"call_id": "call_pwd",
			"action": map[string]any{
				"type":              "exec",
				"command":           []any{"zsh", "-lc", "pwd"},
				"working_directory": "/repo",
			},
		}},
	})

	second := first
	second.Input = []map[string]any{
		{"type": "message", "role": "developer", "content": "reshaped cursor preamble"},
		{"type": "message", "role": "user", "content": "inspect"},
		{
			"type":      "function_call",
			"call_id":   "call_pwd",
			"name":      "Shell",
			"arguments": `{"working_directory":"/repo","command":"pwd","block_until_ms":1000}`,
		},
		{"type": "message", "role": "user", "content": "next"},
	}
	decision := store.Prepare(second)
	if !decision.Hit {
		t.Fatalf("expected continuation hit, miss_reason=%q", decision.MissReason)
	}
	if len(decision.IncrementalInput) != 1 || itemRole(decision.IncrementalInput[0]) != "user" {
		t.Fatalf("incremental_input=%v", decision.IncrementalInput)
	}
}

func TestContinuationToolEventsCanonicalizeShellArgumentAliases(t *testing.T) {
	a := map[string]any{
		"type":      "function_call",
		"name":      "shell_command",
		"arguments": `{"command":"pwd","workdir":"/repo","timeout_ms":1000}`,
	}
	b := map[string]any{
		"type":      "function_call",
		"name":      "Shell",
		"arguments": `{"working_directory":"/repo","command":"pwd","block_until_ms":1000}`,
	}
	if !continuationItemEqual(a, b) {
		t.Fatalf("expected canonical shell calls to match")
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
