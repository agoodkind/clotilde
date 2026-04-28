package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

type ContinuationStore struct {
	mu       sync.Mutex
	entries  map[string]ContinuationEntry
	hitRates *HitRateTracker
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
	Key                  string
	Hit                  bool
	MissReason           string
	MismatchField        string
	FingerprintMatch     bool
	StoredFingerprint    string
	IncomingFingerprint  string
	PreviousResponseID   string
	IncrementalInput     []map[string]any
	Diagnostics          ContinuationDiagnostics
}

type ContinuationDiagnostics struct {
	ExpectedEventCount int
	CurrentEventCount  int
	MatchStart         int
	MatchEnd           int
	Mismatch           *ContinuationMismatch
}

type ContinuationMismatch struct {
	ExpectedEventIndex int
	CurrentEventIndex  int
	ExpectedItemIndex  int
	CurrentItemIndex   int
	Expected           string
	Current            string
}

func NewContinuationStore() *ContinuationStore {
	return &ContinuationStore{
		entries:  make(map[string]ContinuationEntry),
		hitRates: NewHitRateTracker(defaultHitRateWindow),
	}
}

// RecordHitRate updates the rolling hit-rate window for the given
// conversation key with one new observation and returns the
// post-update rate plus current window size. Callers wire this into
// LogContinuationDecision so per-conversation regression is visible
// from logs alone.
func (s *ContinuationStore) RecordHitRate(conversationKey string, hit bool) (float64, int) {
	if s == nil {
		return 0, 0
	}
	return s.hitRates.Record(conversationKey, hit)
}

func (s *ContinuationStore) Prepare(req ResponseCreateWsRequest) ContinuationDecision {
	key := strings.TrimSpace(req.PromptCacheKey)
	if key == "" {
		return ContinuationDecision{MissReason: "missing_prompt_cache_key", MismatchField: "prompt_cache_key"}
	}
	fingerprint := continuationConfigFingerprint(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return ContinuationDecision{Key: key, MissReason: "no_prior_response", IncomingFingerprint: fingerprint}
	}
	out := ContinuationDecision{
		Key:                 key,
		PreviousResponseID:  entry.PreviousResponseID,
		FingerprintMatch:    entry.ConfigFingerprint == fingerprint && entry.Model == req.Model,
		StoredFingerprint:   entry.ConfigFingerprint,
		IncomingFingerprint: fingerprint,
	}
	if strings.TrimSpace(entry.PreviousResponseID) == "" {
		out.MissReason = "missing_previous_response_id"
		out.MismatchField = "previous_response_id"
		return out
	}
	if !out.FingerprintMatch {
		out.MissReason = "fingerprint_mismatch"
		if entry.Model != req.Model {
			out.MismatchField = "model"
		} else {
			out.MismatchField = "config_fingerprint"
		}
		return out
	}
	diff := incrementalContinuationInput(entry.Input, entry.OutputItems, req.Input)
	out.Diagnostics = diff.Diagnostics
	if len(diff.IncrementalInput) == 0 {
		out.MissReason = diff.Reason
		out.MismatchField = continuationMismatchFieldFromReason(diff.Reason)
		return out
	}
	out.Hit = true
	out.IncrementalInput = diff.IncrementalInput
	return out
}

// continuationMismatchFieldFromReason classifies the diff.Reason
// produced by incrementalContinuationInput into a stable bucket the
// telemetry sink can group on. Reasons that are not yet enumerated
// fall through to "other" so unknown shapes surface as data rather
// than panic the daemon.
func continuationMismatchFieldFromReason(reason string) string {
	switch reason {
	case "output_item_baseline_mismatch":
		return "output_item_baseline"
	case "input_baseline_mismatch":
		return "input_baseline"
	case "no_incremental_input":
		return "input"
	case "":
		return ""
	}
	return "other"
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

type continuationDiffResult struct {
	IncrementalInput []map[string]any
	Reason           string
	Diagnostics      ContinuationDiagnostics
}

func incrementalContinuationInput(previous, outputItems, current []map[string]any) continuationDiffResult {
	if len(current) == 0 {
		return continuationDiffResult{Reason: "empty_input"}
	}
	if len(outputItems) > 0 {
		return diffContinuationOutputBaseline(outputItems, current)
	}
	if len(previous) > 0 && len(current) > len(previous) && inputPrefixEqual(previous, current) {
		return continuationDiffResult{IncrementalInput: cloneInput(current[len(previous):])}
	}
	for i := len(current) - 1; i >= 0; i-- {
		if itemRole(current[i]) == "assistant" && i+1 < len(current) {
			return continuationDiffResult{IncrementalInput: cloneInput(current[i+1:])}
		}
	}
	return continuationDiffResult{Reason: "no_incremental_input"}
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

func continuationItemEqual(a, b map[string]any) bool {
	if aEvent, ok := canonicalContinuationEvent(a); ok {
		if bEvent, ok := canonicalContinuationEvent(b); ok {
			if aEvent.Identity != "" || bEvent.Identity != "" {
				return aEvent.Kind == bEvent.Kind && aEvent.Identity != "" && aEvent.Identity == bEvent.Identity
			}
			return aEvent == bEvent
		}
	}
	return jsonEqual(canonicalContinuationItem(a), canonicalContinuationItem(b))
}

type indexedContinuationEvent struct {
	Event     continuationEvent
	ItemIndex int
}

func diffContinuationOutputBaseline(outputItems, current []map[string]any) continuationDiffResult {
	expected := indexedContinuationEvents(outputItems)
	actual := indexedContinuationEvents(current)
	out := continuationDiffResult{
		Diagnostics: ContinuationDiagnostics{
			ExpectedEventCount: len(expected),
			CurrentEventCount:  len(actual),
			MatchStart:         -1,
			MatchEnd:           -1,
		},
	}
	if len(expected) == 0 {
		out.Reason = "output_item_baseline_without_events"
		return out
	}
	if len(actual) < len(expected) {
		out.Reason = "output_item_baseline_mismatch"
		out.Diagnostics.Mismatch = continuationMismatchAt(expected, actual, 0, 0)
		return out
	}
	for start := len(actual) - len(expected); start >= 0; start-- {
		if continuationEventSequenceEqual(expected, actual[start:start+len(expected)]) {
			last := actual[start+len(expected)-1]
			tailStart := last.ItemIndex + 1
			out.Diagnostics.MatchStart = actual[start].ItemIndex
			out.Diagnostics.MatchEnd = tailStart
			if tailStart < len(current) {
				out.IncrementalInput = cloneInput(current[tailStart:])
				return out
			}
			out.Reason = "no_incremental_input_after_output_baseline"
			return out
		}
	}
	out.Reason = "output_item_baseline_mismatch"
	bestStart, matched := continuationBestEventPrefix(expected, actual)
	out.Diagnostics.Mismatch = continuationMismatchAt(expected, actual, matched, bestStart+matched)
	return out
}

func indexedContinuationEvents(items []map[string]any) []indexedContinuationEvent {
	out := make([]indexedContinuationEvent, 0, len(items))
	for idx, item := range items {
		event, ok := canonicalContinuationEvent(item)
		if !ok {
			event = continuationEvent{
				Kind:    "raw:" + strings.TrimSpace(mapString(item, "type")),
				Payload: canonicalContinuationJSON(canonicalContinuationItem(item)),
			}
		}
		out = append(out, indexedContinuationEvent{Event: event, ItemIndex: idx})
	}
	return out
}

func continuationEventSequenceEqual(expected, actual []indexedContinuationEvent) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i := range expected {
		if !continuationEventsEqual(expected[i].Event, actual[i].Event) {
			return false
		}
	}
	return true
}

func continuationEventsEqual(a, b continuationEvent) bool {
	if a.Identity != "" || b.Identity != "" {
		return a.Kind == b.Kind && a.Identity != "" && a.Identity == b.Identity
	}
	return a == b
}

func continuationBestEventPrefix(expected, actual []indexedContinuationEvent) (int, int) {
	bestStart := 0
	bestMatched := 0
	for start := range actual {
		matched := 0
		for matched < len(expected) && start+matched < len(actual) {
			if !continuationEventsEqual(expected[matched].Event, actual[start+matched].Event) {
				break
			}
			matched++
		}
		if matched > bestMatched {
			bestStart = start
			bestMatched = matched
		}
	}
	if bestMatched == 0 && len(expected) > 0 {
		for start := range actual {
			if actual[start].Event.Kind == expected[0].Event.Kind {
				return start, 0
			}
		}
	}
	return bestStart, bestMatched
}

func continuationMismatchAt(expected, actual []indexedContinuationEvent, expectedIdx, actualIdx int) *ContinuationMismatch {
	mismatch := &ContinuationMismatch{
		ExpectedEventIndex: expectedIdx,
		CurrentEventIndex:  actualIdx,
		ExpectedItemIndex:  -1,
		CurrentItemIndex:   -1,
		Expected:           "<end>",
		Current:            "<end>",
	}
	if expectedIdx >= 0 && expectedIdx < len(expected) {
		mismatch.ExpectedItemIndex = expected[expectedIdx].ItemIndex
		mismatch.Expected = expected[expectedIdx].Event.Summary()
	}
	if actualIdx >= 0 && actualIdx < len(actual) {
		mismatch.CurrentItemIndex = actual[actualIdx].ItemIndex
		mismatch.Current = actual[actualIdx].Event.Summary()
	}
	return mismatch
}

type continuationEvent struct {
	Kind     string
	Identity string
	Role     string
	Name     string
	Text     string
	Payload  string
}

func canonicalContinuationEvent(item map[string]any) (continuationEvent, bool) {
	itemType := strings.TrimSpace(mapString(item, "type"))
	switch itemType {
	case "message":
		return continuationEvent{
			Kind: "message",
			Role: strings.ToLower(strings.TrimSpace(mapString(item, "role"))),
			Text: continuationContentText(item["content"]),
		}, true
	case "function_call":
		name := InboundToolName(mapString(item, "name"))
		payload := canonicalContinuationString(mapString(item, "arguments"))
		if IsShellToolName(name) {
			payload = canonicalContinuationShellArguments(mapString(item, "arguments"))
		}
		return continuationEvent{
			Kind:     "tool_call",
			Identity: strings.TrimSpace(mapString(item, "call_id")),
			Name:     name,
			Payload:  payload,
		}, true
	case "local_shell_call":
		return continuationEvent{
			Kind:     "tool_call",
			Identity: strings.TrimSpace(mapString(item, "call_id")),
			Name:     "Shell",
			Payload:  canonicalContinuationLocalShellAction(item["action"]),
		}, true
	case "custom_tool_call":
		return continuationEvent{
			Kind:     "tool_call",
			Identity: strings.TrimSpace(mapString(item, "call_id")),
			Name:     InboundToolName(mapString(item, "name")),
			Payload:  rawString(item, "input"),
		}, true
	case "function_call_output", "custom_tool_call_output":
		return continuationEvent{
			Kind:     "tool_output",
			Identity: strings.TrimSpace(mapString(item, "call_id")),
			Text:     continuationOutputText(item["output"]),
		}, true
	case "reasoning":
		return continuationEvent{
			Kind:     "reasoning",
			Identity: strings.TrimSpace(mapString(item, "id")),
			Text:     continuationReasoningText(item),
			Payload:  rawString(item, "encrypted_content"),
		}, true
	default:
		return continuationEvent{}, false
	}
}

func (e continuationEvent) Summary() string {
	parts := []string{"kind=" + e.Kind}
	if e.Identity != "" {
		parts = append(parts, "id="+e.Identity)
	}
	if e.Role != "" {
		parts = append(parts, "role="+e.Role)
	}
	if e.Name != "" {
		parts = append(parts, "name="+e.Name)
	}
	if e.Text != "" {
		parts = append(parts, "text_len="+strconv.Itoa(len(e.Text)))
	}
	if e.Payload != "" {
		parts = append(parts, "payload_len="+strconv.Itoa(len(e.Payload)))
	}
	return strings.Join(parts, " ")
}

func canonicalContinuationItem(item map[string]any) map[string]any {
	itemType := strings.TrimSpace(mapString(item, "type"))
	out := map[string]any{"type": itemType}
	switch itemType {
	case "message":
		out["role"] = strings.ToLower(strings.TrimSpace(mapString(item, "role")))
		out["text"] = continuationContentText(item["content"])
	case "function_call":
		out["call_id"] = strings.TrimSpace(mapString(item, "call_id"))
		out["name"] = InboundToolName(mapString(item, "name"))
		out["arguments"] = canonicalContinuationString(mapString(item, "arguments"))
	case "function_call_output":
		out["call_id"] = strings.TrimSpace(mapString(item, "call_id"))
		out["output"] = continuationOutputText(item["output"])
	case "custom_tool_call":
		out["call_id"] = strings.TrimSpace(mapString(item, "call_id"))
		out["name"] = InboundToolName(mapString(item, "name"))
		out["input"] = rawString(item, "input")
	case "custom_tool_call_output":
		out["call_id"] = strings.TrimSpace(mapString(item, "call_id"))
		out["name"] = InboundToolName(mapString(item, "name"))
		out["output"] = continuationOutputText(item["output"])
	case "local_shell_call":
		out["call_id"] = strings.TrimSpace(mapString(item, "call_id"))
		out["action"] = item["action"]
	default:
		for k, v := range item {
			switch k {
			case "id", "status":
				continue
			default:
				out[k] = v
			}
		}
	}
	return out
}

func continuationContentText(raw any) string {
	if text := responsesContentText(raw); text != "" {
		return text
	}
	switch v := raw.(type) {
	case []map[string]any:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			switch strings.TrimSpace(mapString(part, "type")) {
			case "text", "input_text", "output_text":
				if text := rawString(part, "text"); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return SanitizeForUpstreamCache(strings.Join(parts, "\n"))
	}
	return ""
}

func continuationOutputText(raw any) string {
	if text := responsesOutputText(raw); text != "" {
		return text
	}
	if text := continuationContentText(raw); text != "" {
		return text
	}
	return ""
}

func continuationReasoningText(item map[string]any) string {
	if text := continuationContentText(item["summary"]); text != "" {
		return text
	}
	if text := continuationContentText(item["content"]); text != "" {
		return text
	}
	raw := map[string]any{}
	if summary, ok := item["summary"]; ok {
		raw["summary"] = summary
	}
	if content, ok := item["content"]; ok {
		raw["content"] = content
	}
	if len(raw) == 0 {
		return ""
	}
	return canonicalContinuationJSON(raw)
}

func canonicalContinuationString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return raw
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func canonicalContinuationShellArguments(raw string) string {
	args := ToolCallArgsMap(raw)
	if args == nil {
		return canonicalContinuationString(raw)
	}
	out := map[string]any{}
	if command := StringArg(args, "command", "cmd"); command != "" {
		out["command"] = command
	}
	if workdir := StringArg(args, "workdir", "working_directory", "cwd"); workdir != "" {
		out["workdir"] = workdir
	}
	if timeout, ok := NumberArg(args, "timeout_ms", "block_until_ms"); ok {
		out["timeout_ms"] = timeout
	}
	return canonicalContinuationJSON(out)
}

func canonicalContinuationLocalShellAction(raw any) string {
	action, _ := raw.(map[string]any)
	if action == nil {
		return ""
	}
	out := map[string]any{}
	if command := localShellActionCommand(action["command"]); command != "" {
		out["command"] = command
	}
	if workdir := StringArg(action, "working_directory", "workdir", "cwd"); workdir != "" {
		out["workdir"] = workdir
	}
	if timeout, ok := NumberArg(action, "timeout_ms", "block_until_ms"); ok {
		out["timeout_ms"] = timeout
	}
	return canonicalContinuationJSON(out)
}

func localShellActionCommand(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		if len(v) >= 3 {
			if flag, _ := v[len(v)-2].(string); flag == "-lc" {
				if command, _ := v[len(v)-1].(string); strings.TrimSpace(command) != "" {
					return strings.TrimSpace(command)
				}
			}
		}
		parts := make([]string, 0, len(v))
		for _, part := range v {
			if text, _ := part.(string); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	case []string:
		if len(v) >= 3 && v[len(v)-2] == "-lc" {
			return strings.TrimSpace(v[len(v)-1])
		}
		return strings.TrimSpace(strings.Join(v, " "))
	default:
		return ""
	}
}

func canonicalContinuationJSON(raw any) string {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return string(encoded)
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
