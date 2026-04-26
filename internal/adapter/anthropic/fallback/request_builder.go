package fallback

import (
	"encoding/json"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

// RequestBuildInput is the OpenAI-compatible request surface the
// fallback backend needs in order to build a Claude CLI request.
type RequestBuildInput struct {
	Model      string
	ModelAlias string
	Messages   []adapteropenai.ChatMessage
	Tools      []adapteropenai.Tool
	Functions  []adapteropenai.Function
	ToolChoice json.RawMessage
	RequestID  string
}

// BuildRequest maps an OpenAI-compatible chat request into the
// Claude CLI fallback request shape. The root adapter supplies model
// routing and response-format additions; this package owns the
// fallback-specific message, tool, tool_choice, and session-id
// conventions.
func BuildRequest(in RequestBuildInput) Request {
	system, msgs := BuildMessages(in.Messages)
	return Request{
		Model:      in.Model,
		System:     system,
		Messages:   msgs,
		Tools:      BuildTools(in.Tools, in.Functions),
		ToolChoice: ParseToolChoice(in.ToolChoice),
		RequestID:  in.RequestID,
		SessionID:  DeriveSessionID(firstUserMessage(msgs), in.ModelAlias),
	}
}

// BuildTools maps OpenAI tools (preferred) or compatibility functions
// into the fallback tool slice. When tools is non-empty,
// compatibility functions are ignored so definitions are not
// double-registered.
func BuildTools(tools []adapteropenai.Tool, functions []adapteropenai.Function) []Tool {
	if len(tools) > 0 {
		out := make([]Tool, 0, len(tools))
		for _, t := range tools {
			if t.Function.Name == "" {
				continue
			}
			out = append(out, Tool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
		return out
	}
	out := make([]Tool, 0, len(functions))
	for _, f := range functions {
		if f.Name == "" {
			continue
		}
		out = append(out, Tool{
			Name:        f.Name,
			Description: f.Description,
			Parameters:  f.Parameters,
		})
	}
	return out
}

// ParseToolChoice decodes OpenAI tool_choice as either a string token
// or a typed function selection object.
func ParseToolChoice(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "auto"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	var wrapped struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		if wrapped.Type == "function" && strings.TrimSpace(wrapped.Function.Name) != "" {
			return strings.TrimSpace(wrapped.Function.Name)
		}
	}
	return "auto"
}

// BuildMessages converts OpenAI-shaped chat messages into the
// fallback package's Message slice. Multiple system/developer
// messages are joined; tool/function turns are folded into the user
// lane the same way the direct Anthropic backend does it.
func BuildMessages(in []adapteropenai.ChatMessage) (string, []Message) {
	var sys []string
	var out []Message
	for _, m := range in {
		text := adapteropenai.FlattenContent(m.Content)
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if text != "" {
				sys = append(sys, text)
			}
		case "user", "assistant":
			out = appendOrMergeMessage(out, role, text)
		case "tool", "function":
			out = appendOrMergeMessage(out, "user", "tool: "+text)
		default:
			out = appendOrMergeMessage(out, "user", role+": "+text)
		}
	}
	return joinNonEmpty(sys, "\n\n"), out
}

func appendOrMergeMessage(msgs []Message, role, text string) []Message {
	if text == "" {
		return msgs
	}
	if n := len(msgs); n > 0 && msgs[n-1].Role == role {
		msgs[n-1].Content = msgs[n-1].Content + "\n\n" + text
		return msgs
	}
	return append(msgs, Message{Role: role, Content: text})
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i > 0 && out != "" {
			out += sep
		}
		out += p
	}
	return out
}
