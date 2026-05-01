package codex

import (
	"encoding/json"
	"strings"
)

// continuationEvent is the canonical shape used to compare two
// Codex Responses input items for equivalence. The event extracts
// the identity-bearing fields (kind, call_id, response_id) so
// items that round-trip through canonicalization compare equal
// even when their raw representations diverge in incidental
// fields. Used by ComputeDelta in delta_input.go.
type continuationEvent struct {
	Kind     string
	Identity string
	Role     string
	Name     string
	Text     string
	Payload  string
}

// continuationItemEqual reports whether two Codex input items refer
// to the same logical event. Identity-bearing items compare on
// kind+identity. Items without identity compare on canonical JSON.
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

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
