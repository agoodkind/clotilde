package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
)

type ContinuationStore struct {
	mu      sync.Mutex
	entries map[string]ContinuationEntry
}

type ContinuationEntry struct {
	Key                string
	Model              string
	ConfigFingerprint  string
	PreviousResponseID string
	Input              []map[string]any
	OutputItems        []map[string]any
}

type ContinuationDecision struct {
	Key                string
	Hit                bool
	MissReason         string
	FingerprintMatch   bool
	PreviousResponseID string
	IncrementalInput   []map[string]any
}

func NewContinuationStore() *ContinuationStore {
	return &ContinuationStore{entries: make(map[string]ContinuationEntry)}
}

func (s *ContinuationStore) Prepare(req ResponseCreateWsRequest) ContinuationDecision {
	key := strings.TrimSpace(req.PromptCacheKey)
	if key == "" {
		return ContinuationDecision{MissReason: "missing_prompt_cache_key"}
	}
	fingerprint := continuationConfigFingerprint(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return ContinuationDecision{Key: key, MissReason: "no_prior_response"}
	}
	out := ContinuationDecision{
		Key:                key,
		PreviousResponseID: entry.PreviousResponseID,
		FingerprintMatch:   entry.ConfigFingerprint == fingerprint && entry.Model == req.Model,
	}
	if strings.TrimSpace(entry.PreviousResponseID) == "" {
		out.MissReason = "missing_previous_response_id"
		return out
	}
	if !out.FingerprintMatch {
		out.MissReason = "fingerprint_mismatch"
		return out
	}
	incremental, reason := incrementalContinuationInput(entry.Input, entry.OutputItems, req.Input)
	if len(incremental) == 0 {
		out.MissReason = reason
		return out
	}
	out.Hit = true
	out.IncrementalInput = incremental
	return out
}

func (s *ContinuationStore) Complete(decision ContinuationDecision, fullReq ResponseCreateWsRequest, result RunResult) {
	key := strings.TrimSpace(decision.Key)
	if key == "" {
		key = strings.TrimSpace(fullReq.PromptCacheKey)
	}
	if key == "" {
		return
	}
	responseID := strings.TrimSpace(result.ResponseID)
	if responseID == "" {
		s.Forget(key)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = ContinuationEntry{
		Key:                key,
		Model:              fullReq.Model,
		ConfigFingerprint:  continuationConfigFingerprint(fullReq),
		PreviousResponseID: responseID,
		Input:              cloneInput(fullReq.Input),
		OutputItems:        cloneInput(result.OutputItems),
	}
}

func (s *ContinuationStore) Forget(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

func incrementalContinuationInput(previous, outputItems, current []map[string]any) ([]map[string]any, string) {
	if len(current) == 0 {
		return nil, "empty_input"
	}
	if len(outputItems) > 0 {
		baseline := make([]map[string]any, 0, len(previous)+len(outputItems))
		baseline = append(baseline, previous...)
		baseline = append(baseline, outputItems...)
		if len(current) > len(baseline) && inputPrefixEqual(baseline, current) {
			return cloneInput(current[len(baseline):]), ""
		}
		return nil, "output_item_baseline_mismatch"
	}
	if len(previous) > 0 && len(current) > len(previous) && inputPrefixEqual(previous, current) {
		return cloneInput(current[len(previous):]), ""
	}
	for i := len(current) - 1; i >= 0; i-- {
		if itemRole(current[i]) == "assistant" && i+1 < len(current) {
			return cloneInput(current[i+1:]), ""
		}
	}
	return nil, "no_incremental_input"
}

func inputPrefixEqual(previous, current []map[string]any) bool {
	if len(previous) > len(current) {
		return false
	}
	for i := range previous {
		if !jsonEqual(previous[i], current[i]) {
			return false
		}
	}
	return true
}

func itemRole(item map[string]any) string {
	return strings.ToLower(strings.TrimSpace(mapString(item, "role")))
}

func continuationConfigFingerprint(req ResponseCreateWsRequest) string {
	type fingerprintRequest struct {
		Type              string                       `json:"type"`
		Model             string                       `json:"model,omitempty"`
		Instructions      string                       `json:"instructions,omitempty"`
		Tools             []any                        `json:"tools,omitempty"`
		ToolChoice        string                       `json:"tool_choice,omitempty"`
		ParallelToolCalls bool                         `json:"parallel_tool_calls,omitempty"`
		Reasoning         *Reasoning                   `json:"reasoning,omitempty"`
		Include           []string                     `json:"include,omitempty"`
		ServiceTier       string                       `json:"service_tier,omitempty"`
		PromptCacheKey    string                       `json:"prompt_cache_key,omitempty"`
		Text              any                          `json:"text,omitempty"`
		ClientMetadata    ResponseCreateClientMetadata `json:"client_metadata,omitempty"`
	}
	raw, _ := json.Marshal(fingerprintRequest{
		Type:              req.Type,
		Model:             req.Model,
		Instructions:      req.Instructions,
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Reasoning:         req.Reasoning,
		Include:           req.Include,
		ServiceTier:       req.ServiceTier,
		PromptCacheKey:    req.PromptCacheKey,
		Text:              req.Text,
		ClientMetadata:    req.ClientMetadata,
	})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

func cloneInput(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, len(in))
	for i, item := range in {
		raw, _ := json.Marshal(item)
		var cloned map[string]any
		_ = json.Unmarshal(raw, &cloned)
		out[i] = cloned
	}
	return out
}

func cloneMap(item map[string]any) map[string]any {
	if item == nil {
		return nil
	}
	raw, _ := json.Marshal(item)
	var cloned map[string]any
	_ = json.Unmarshal(raw, &cloned)
	return cloned
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
