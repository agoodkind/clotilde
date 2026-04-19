package fallback

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Tool is one function tool forwarded from the OpenAI request shape.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// normalizeToolChoice maps empty to auto and lowercases known tokens.
func normalizeToolChoice(choice string) string {
	c := strings.TrimSpace(strings.ToLower(choice))
	if c == "" {
		return "auto"
	}
	return c
}

// renderToolsPreamble returns text appended to the system prompt when
// tool-injected calling is active. For choice "none" it returns empty
// so the model never sees tool JSON Schema in the system prompt.
func renderToolsPreamble(tools []Tool, choice string) string {
	if len(tools) == 0 {
		return ""
	}
	c := normalizeToolChoice(choice)
	if c == "none" {
		return ""
	}
	list := tools
	extra := ""
	switch c {
	case "required":
		extra = "You MUST call exactly one tool. Do not respond in plain text."
	case "auto":
		// list all tools, no extra constraint
	default:
		for _, t := range tools {
			if t.Name == c {
				list = []Tool{t}
				break
			}
		}
		extra = "You MUST call the tool named " + c + "."
	}
	var b strings.Builder
	b.WriteString("Available tools:\n\n")
	for _, t := range list {
		b.WriteString("- name: ")
		b.WriteString(t.Name)
		b.WriteString("\n  description: ")
		b.WriteString(strings.TrimSpace(t.Description))
		b.WriteString("\n  parameters (JSON Schema): ")
		if len(t.Parameters) == 0 {
			b.WriteString("{}")
		} else {
			b.WriteString(string(bytes.TrimSpace(t.Parameters)))
		}
		b.WriteString("\n\n")
	}
	b.WriteString(
		"When you decide to call a tool, your ENTIRE response MUST be a single JSON object on one line " +
			"of the exact form: {\"tool_calls\":[{\"name\":\"<tool_name>\",\"arguments\":{...}}]}. " +
			"Do not wrap in markdown. Do not include any other text. " +
			"To call multiple tools, include multiple objects in the array. " +
			"To answer without calling a tool, respond as plain text without any JSON envelope.\n",
	)
	if extra != "" {
		b.WriteString("\n")
		b.WriteString(extra)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ToolCall is one parsed tool invocation in OpenAI wire shape
// (arguments is a JSON string).
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type toolEnvelopeEntry struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolEnvelopeWire struct {
	ToolCalls []toolEnvelopeEntry `json:"tool_calls"`
}

// parseToolEnvelope inspects the last non-empty line that starts with
// "{" and ends with "}" and treats it as a tool envelope. On success
// ok is true, toolCalls have empty IDs (assign in the caller), and
// prefixText is the assistant text before that line.
func parseToolEnvelope(buf string) (toolCalls []ToolCall, prefixText string, ok bool) {
	normalized := strings.ReplaceAll(buf, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	envIdx := -1
	var envLine string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			envIdx = i
			envLine = line
			break
		}
	}
	if envIdx < 0 {
		return nil, "", false
	}
	prefixText = strings.Join(lines[:envIdx], "\n")
	var wire toolEnvelopeWire
	if err := json.Unmarshal([]byte(envLine), &wire); err != nil {
		return nil, "", false
	}
	if len(wire.ToolCalls) == 0 {
		return nil, "", false
	}
	out := make([]ToolCall, 0, len(wire.ToolCalls))
	for _, e := range wire.ToolCalls {
		if strings.TrimSpace(e.Name) == "" {
			return nil, "", false
		}
		argStr, err := coerceArgumentsToJSONString(e.Arguments)
		if err != nil {
			return nil, "", false
		}
		out = append(out, ToolCall{Name: e.Name, Arguments: argStr})
	}
	return out, prefixText, true
}

func coerceArgumentsToJSONString(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "{}", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// EnsureToolCallID returns id when non-empty; otherwise a deterministic
// OpenAI-shaped tool_calls[].id (call_<requestID>_<index>).
func EnsureToolCallID(id, reqID string, idx int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("call_%s_%d", reqID, idx)
}

// toolEnvelopeActive is true when tools are present and the model was
// given definitions (choice is not "none"), so envelope parsing runs.
func toolEnvelopeActive(r Request) bool {
	return len(r.Tools) > 0 && normalizeToolChoice(r.ToolChoice) != "none"
}

func mergeSystemPrompt(base string, toolsPreamble string) string {
	if toolsPreamble == "" {
		return base
	}
	if base == "" {
		return toolsPreamble
	}
	return base + "\n\n" + toolsPreamble
}

func finalizeAssistantText(fullText, reasoning string, r Request, usage Usage, apiStopReason string) Result {
	out := Result{
		Text:             fullText,
		ReasoningContent: reasoning,
		Usage:            usage,
		Stop:             "stop",
	}
	if strings.EqualFold(apiStopReason, "refusal") {
		out.Refusal = fullText
		out.Text = ""
		out.ReasoningContent = ""
		out.Stop = "refusal"
		return out
	}
	if !toolEnvelopeActive(r) {
		return out
	}
	calls, prefix, parsed := parseToolEnvelope(fullText)
	if !parsed || len(calls) == 0 {
		tail := lastEnvelopeCandidateLine(fullText)
		if tail != "" {
			slog.Debug("fallback.tools.envelope_invalid",
				"request_id", r.RequestID,
				"tail", truncateRunes(tail, 200),
			)
		}
		return out
	}
	for i := range calls {
		calls[i].ID = EnsureToolCallID(calls[i].ID, r.RequestID, i)
	}
	slog.Debug("fallback.tools.envelope_parsed",
		"request_id", r.RequestID,
		"tool_calls", len(calls),
	)
	out.Text = prefix
	out.ToolCalls = calls
	out.Stop = "tool_calls"
	return out
}

func lastEnvelopeCandidateLine(buf string) string {
	normalized := strings.ReplaceAll(buf, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			return line
		}
	}
	return ""
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
