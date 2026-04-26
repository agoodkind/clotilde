package anthropicbackend

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

var (
	// ErrAudioUnsupported is returned when a message includes an input_audio part.
	ErrAudioUnsupported = errors.New("audio content parts are not supported by the Anthropic backend")

	// ErrUnknownToolType is returned when tools[].type is set to a value other than "function".
	ErrUnknownToolType = errors.New("tool.type must be \"function\"")
)

type OpenAIRequest = adapteropenai.ChatRequest
type OpenAITool = adapteropenai.Tool
type OpenAIToolFunctionSchema = adapteropenai.ToolFunctionSchema
type OpenAIFunction = adapteropenai.Function
type OpenAIMessage = adapteropenai.ChatMessage
type OpenAIToolCall = adapteropenai.ToolCall
type OpenAIToolCallFunction = adapteropenai.ToolCallFunction
type OpenAIContentPart = adapteropenai.ContentPart

var noticeSentinelRE = regexp.MustCompile(`(?s)<!--clyde-notice-->.*?<!--/clyde-notice-->\s*`)
var activitySentinelRE = regexp.MustCompile(`(?s)<!--clyde-activity-->.*?<!--/clyde-activity-->\s*`)

// TranslateRequest maps an OpenAI-shaped chat request to Anthropic /v1/messages fields.
func TranslateRequest(req OpenAIRequest, systemPrefix string, maxTokens int) (AnthRequest, error) {
	var systemPieces []string
	var out []AnthMessage

	for msgIdx, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer":
			t := flattenContent(msg.Content)
			if strings.TrimSpace(t) != "" {
				systemPieces = append(systemPieces, t)
			}
		case "user":
			blocks, err := openAIMessageToUserBlocks(msgIdx, msg)
			if err != nil {
				return AnthRequest{}, err
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, AnthMessage{Role: "user", Content: blocks})
		case "assistant":
			blocks, err := openAIMessageToAssistantBlocks(msgIdx, msg)
			if err != nil {
				return AnthRequest{}, err
			}
			if len(blocks) == 0 && len(msg.ToolCalls) == 0 {
				continue
			}
			out = append(out, AnthMessage{Role: "assistant", Content: blocks})
		case "tool", "function":
			result := flattenContent(msg.Content)
			if result == "" {
				result = " "
			}
			out = append(out, AnthMessage{
				Role: "user",
				Content: []AnthContentBlock{{
					Type:          "tool_result",
					ToolUseID:     msg.ToolCallID,
					ResultContent: result,
				}},
			})
		default:
			return AnthRequest{}, fmt.Errorf("unsupported message role %q", msg.Role)
		}
	}

	out = mergeConsecutiveSameRole(out)

	systemJoined := strings.Join(systemPieces, "\n\n")
	systemStr := joinSystem(systemPrefix, systemJoined)

	tools, err := translateTools(req)
	if err != nil {
		return AnthRequest{}, err
	}

	toolChoice, err := translateToolChoice(req.ToolChoice)
	if err != nil {
		return AnthRequest{}, err
	}

	if req.ParallelTools != nil && !*req.ParallelTools {
		if toolChoice == nil {
			toolChoice = &AnthToolChoice{Type: "auto"}
		}
		toolChoice.DisableParallelToolUse = true
	}

	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		toolNames = append(toolNames, t.Name)
	}
	choiceType := ""
	choiceName := ""
	if toolChoice != nil {
		choiceType = toolChoice.Type
		choiceName = toolChoice.Name
	}
	slog.Info("adapter.anthropic.request.translated",
		"subcomponent", "anthropic",
		"model", req.Model,
		"system_len", len(systemStr),
		"message_count", len(out),
		"tool_count", len(tools),
		"tool_names", toolNames,
		"tool_choice_type", choiceType,
		"tool_choice_name", choiceName,
		"stream", req.Stream,
	)

	return AnthRequest{
		Model:      req.Model,
		System:     systemStr,
		Messages:   out,
		MaxTokens:  maxTokens,
		Tools:      tools,
		ToolChoice: toolChoice,
		Stream:     req.Stream,
	}, nil
}

func joinSystem(prefix, collected string) string {
	collected = strings.TrimSpace(collected)
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return collected
	}
	if collected == "" {
		return prefix
	}
	if strings.HasPrefix(collected, prefix) {
		return collected
	}
	return prefix + "\n\n" + collected
}

func mergeConsecutiveSameRole(in []AnthMessage) []AnthMessage {
	if len(in) <= 1 {
		return in
	}
	out := make([]AnthMessage, 0, len(in))
	cur := in[0]
	for i := 1; i < len(in); i++ {
		next := in[i]
		if next.Role == cur.Role {
			cur.Content = append(cur.Content, next.Content...)
			continue
		}
		out = append(out, cur)
		cur = next
	}
	out = append(out, cur)
	return out
}

func openAIMessageToUserBlocks(msgIdx int, msg OpenAIMessage) ([]AnthContentBlock, error) {
	parts, _ := normalizeContent(msg.Content)
	var blocks []AnthContentBlock
	for partIdx, p := range parts {
		switch p.Type {
		case "text":
			if p.Text == "" {
				continue
			}
			blocks = append(blocks, AnthContentBlock{Type: "text", Text: p.Text})
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			src, err := imageURLToSource(p.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, AnthContentBlock{Type: "image", Source: src})
		case "input_audio":
			return nil, fmt.Errorf("%w: message %d part %d", ErrAudioUnsupported, msgIdx, partIdx)
		case "refusal":
			if p.Refusal == "" {
				continue
			}
			blocks = append(blocks, AnthContentBlock{Type: "text", Text: p.Refusal})
		case "tool_result":
			result := flattenToolResultContent(p.Content)
			if result == "" {
				result = " "
			}
			blocks = append(blocks, AnthContentBlock{
				Type:          "tool_result",
				ToolUseID:     p.ToolUseID,
				ResultContent: result,
			})
			slog.Debug("adapter.anthropic.tool_result.translated",
				"subcomponent", "anthropic",
				"msg_idx", msgIdx,
				"part_idx", partIdx,
				"tool_use_id", p.ToolUseID,
				"content_bytes", len(result),
				"carrier", "user_part",
			)
		default:
			slog.Warn("adapter.anthropic.user_part.unknown_type",
				"subcomponent", "anthropic",
				"msg_idx", msgIdx,
				"part_idx", partIdx,
				"part_type", p.Type,
			)
			blocks = append(blocks, AnthContentBlock{Type: "text", Text: "[" + p.Type + "]"})
		}
	}
	return blocks, nil
}

func stripNotice(text string, msgIdx, partIdx int) string {
	if text == "" {
		return ""
	}
	stripped := noticeSentinelRE.ReplaceAllString(text, "")
	if stripped == text {
		return text
	}
	slog.Info("tooltrans.notice.stripped",
		"subcomponent", "tooltrans",
		"msg_idx", msgIdx,
		"part_idx", partIdx,
		"source_len", len(text),
		"stripped_len", len(stripped),
	)
	return stripped
}

// StripNoticeSentinel removes the clyde notice envelope.
func StripNoticeSentinel(text string) string {
	if text == "" {
		return ""
	}
	return noticeSentinelRE.ReplaceAllString(text, "")
}

// StripActivitySentinel removes the shared activity envelope.
func StripActivitySentinel(text string) string {
	if text == "" {
		return ""
	}
	return activitySentinelRE.ReplaceAllString(text, "")
}

// flattenToolResultContent normalizes a tool_result content payload to a
// single string. Cursor sends either a raw string or an array of OpenAI
// content parts (text-only); both shapes survive the trip.
func flattenToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []OpenAIContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

func openAIMessageToAssistantBlocks(msgIdx int, msg OpenAIMessage) ([]AnthContentBlock, error) {
	parts, _ := normalizeContent(msg.Content)
	var blocks []AnthContentBlock
	for partIdx, p := range parts {
		switch p.Type {
		case "text":
			text := stripNotice(p.Text, msgIdx, partIdx)
			// Strip the "Thinking" blockquote we injected into the
			// stream on the previous turn. Our adapter renders
			// thinking_delta events as markdown blockquote content so
			// Cursor shows the reasoning during streaming. When
			// Cursor sends the chat history back on the next turn
			// that blockquote rides along as part of the assistant
			// message. If we forwarded it verbatim to Anthropic we
			// would (a) bust the prompt cache because the prior
			// assistant bytes no longer match what Anthropic
			// originally produced, (b) re-bill the thinking as
			// visible tokens every turn, and (c) confuse the model
			// by feeding it its own internal reasoning as prior
			// output. Stripping here restores the clean answer only
			// and keeps the cached prefix byte-stable across turns.
			text = stripThinkingBlockquote(text)
			if strings.TrimSpace(text) == "" {
				continue
			}
			blocks = append(blocks, AnthContentBlock{Type: "text", Text: text})
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			src, err := imageURLToSource(p.ImageURL.URL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, AnthContentBlock{Type: "image", Source: src})
		case "input_audio":
			return nil, fmt.Errorf("%w: message %d part %d", ErrAudioUnsupported, msgIdx, partIdx)
		case "refusal":
			refusal := stripNotice(p.Refusal, msgIdx, partIdx)
			if strings.TrimSpace(refusal) == "" {
				continue
			}
			blocks = append(blocks, AnthContentBlock{Type: "text", Text: refusal})
		case "tool_use":
			input := p.Input
			if len(input) == 0 {
				input = json.RawMessage("{}")
			}
			blocks = append(blocks, AnthContentBlock{
				Type:  "tool_use",
				ID:    p.ID,
				Name:  p.Name,
				Input: input,
			})
			slog.Debug("adapter.anthropic.tool_use.translated",
				"subcomponent", "anthropic",
				"msg_idx", msgIdx,
				"part_idx", partIdx,
				"tool_use_id", p.ID,
				"tool_name", p.Name,
				"input_bytes", len(input),
				"carrier", "assistant_part",
			)
		case "thinking":
			continue
		default:
			slog.Warn("adapter.anthropic.assistant_part.unknown_type",
				"subcomponent", "anthropic",
				"msg_idx", msgIdx,
				"part_idx", partIdx,
				"part_type", p.Type,
			)
			continue
		}
	}
	for _, tc := range msg.ToolCalls {
		raw := toolCallArgumentsJSON(tc.Function.Arguments)
		blocks = append(blocks, AnthContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: raw,
		})
	}
	return blocks, nil
}

func imageURLToSource(rawURL string) (*AnthImageSource, error) {
	if strings.HasPrefix(rawURL, "data:") {
		media, data, err := parseDataURI(rawURL)
		if err != nil {
			return nil, err
		}
		return &AnthImageSource{Type: "base64", MediaType: media, Data: data}, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported image url scheme: %q", u.Scheme)
	}
	return &AnthImageSource{Type: "url", URL: rawURL}, nil
}

func parseDataURI(s string) (mediaType string, data string, err error) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return "", "", fmt.Errorf("not a data uri")
	}
	rest := strings.TrimPrefix(s, prefix)
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", "", fmt.Errorf("invalid data uri: missing comma")
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	parts := strings.Split(meta, ";")
	if len(parts) == 0 || parts[0] == "" {
		mediaType = "application/octet-stream"
	} else {
		mediaType = parts[0]
	}
	isBase64 := false
	for _, p := range parts[1:] {
		if p == "base64" {
			isBase64 = true
		}
	}
	if !isBase64 {
		return "", "", fmt.Errorf("data uri must be base64-encoded for images")
	}
	return mediaType, payload, nil
}

func translateTools(req OpenAIRequest) ([]AnthTool, error) {
	var out []AnthTool
	for _, t := range req.Tools {
		if t.Type != "" && t.Type != "function" {
			return nil, ErrUnknownToolType
		}
		out = append(out, AnthTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		})
	}
	for _, f := range req.Functions {
		out = append(out, AnthTool{
			Name:        f.Name,
			Description: f.Description,
			InputSchema: f.Parameters,
		})
	}
	return out, nil
}

func toolCallArgumentsJSON(arguments string) json.RawMessage {
	s := strings.TrimSpace(arguments)
	if s == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(arguments)
}

func translateToolChoice(raw json.RawMessage) (*AnthToolChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch s {
		case "none":
			return &AnthToolChoice{Type: "none"}, nil
		case "auto":
			return &AnthToolChoice{Type: "auto"}, nil
		case "required":
			return &AnthToolChoice{Type: "any"}, nil
		default:
			return &AnthToolChoice{Type: "auto"}, nil
		}
	}
	var obj struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if obj.Type == "function" {
		return &AnthToolChoice{Type: "tool", Name: obj.Function.Name}, nil
	}
	return &AnthToolChoice{Type: "auto"}, nil
}

// thinkingBlockquoteRE matches the "Thinking" collapsible the
// adapter injects into assistant content during streaming. The
// sentinel comment pair (<!--clyde-thinking-->…<!--/clyde-thinking-->)
// makes the block unambiguously ours so we do not risk stripping a
// legitimate <details> the user or model embedded in the answer.
// The (?s) flag lets the inner . match newlines because the wrapper
// spans multiple lines by construction.
var thinkingBlockquoteRE = regexp.MustCompile(`(?s)<!--clyde-thinking-->.*?<!--/clyde-thinking-->\s*`)

// stripThinkingBlockquote removes every clyde-thinking sentinel
// block from text. Matches the envelope anywhere in the assistant
// message (not just at the start) so multi-block answers with
// interleaved thinking all get cleaned. Idempotent: returns text
// unchanged when the sentinel is absent.
func stripThinkingBlockquote(text string) string {
	if !strings.Contains(text, "<!--clyde-thinking-->") {
		// Fast path for the common case (no thinking wrapper in
		// this turn).
		return text
	}
	return thinkingBlockquoteRE.ReplaceAllString(text, "")
}

// StripThinkingSentinel removes the clyde thinking envelope.
func StripThinkingSentinel(text string) string {
	return stripThinkingBlockquote(text)
}
