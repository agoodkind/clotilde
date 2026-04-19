package adapter

import (
	"encoding/json"
	"fmt"
)

const (
	chatBodySummaryMessageLimit = 3
	maxContentChars             = 2048
)

// BodySummary is the compact summary written for adapter.chat.raw when
// logging.body.mode is set to summary or whitelist.
type BodySummary struct {
	Model             string          `json:"model,omitempty"`
	Stream            bool            `json:"stream"`
	MessageCount      int             `json:"message_count"`
	MessagesChars     int             `json:"messages_chars"`
	Messages          []MsgSummary    `json:"messages"`
	Tools             []ToolSummary   `json:"tools"`
	ToolCount         int             `json:"tool_count"`
	ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool           `json:"parallel_tool_calls,omitempty"`
	Logprobs          *bool           `json:"logprobs,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	MaxTokens         *int            `json:"max_tokens,omitempty"`
}

// MsgSummary is a compact representation of a single request message.
type MsgSummary struct {
	Role          string `json:"role"`
	ContentChars  int    `json:"content_chars"`
	HasToolCalls  bool   `json:"has_tool_calls,omitempty"`
	ToolCallCount int    `json:"tool_call_count,omitempty"`
	ToolCallID    string `json:"tool_call_id,omitempty"`
}

// ToolSummary records per-tool metadata without full schema details.
type ToolSummary struct {
	Name        string `json:"name"`
	ParamsChars int    `json:"params_chars"`
}

// SummarizeChatBody returns a compact summary from raw chat request bytes.
func SummarizeChatBody(raw []byte) (BodySummary, error) {
	var req ChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return BodySummary{}, fmt.Errorf("invalid chat request: %w", err)
	}
	return SummarizeChatRequest(req), nil
}

// SummarizeChatRequest returns a compact summary for an already-parsed request.
func SummarizeChatRequest(req ChatRequest) BodySummary {
	toolChoice := req.ToolChoice
	if string(toolChoice) == "null" {
		toolChoice = nil
	}

	summary := BodySummary{
		Model:             req.Model,
		Stream:            req.Stream,
		MessageCount:      len(req.Messages),
		ToolChoice:        toolChoice,
		ParallelToolCalls: req.ParallelTools,
		Logprobs:          req.Logprobs,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		MaxTokens:         req.MaxTokens,
	}

	summary.ToolCount = len(req.Tools) + len(req.Functions)
	summary.Tools = make([]ToolSummary, 0, summary.ToolCount)
	for _, tool := range req.Tools {
		summary.Tools = append(summary.Tools, ToolSummary{
			Name:        tool.Function.Name,
			ParamsChars: len(tool.Function.Parameters),
		})
	}
	for _, fn := range req.Functions {
		summary.Tools = append(summary.Tools, ToolSummary{
			Name:        fn.Name,
			ParamsChars: len(fn.Parameters),
		})
	}

	msgSummaries := summarizeMessages(req.Messages)
	summary.Messages = msgSummaries
	for _, msg := range msgSummaries {
		summary.MessagesChars += msg.ContentChars
	}
	return summary
}

func summarizeMessages(messages []ChatMessage) []MsgSummary {
	samples := make([]MsgSummary, 0, min(chatBodySummaryMessageLimit*2, len(messages)))
	for _, msg := range sampleMessages(messages) {
		samples = append(samples, summarizeMessage(msg))
	}
	return samples
}

func sampleMessages(messages []ChatMessage) []ChatMessage {
	if len(messages) <= chatBodySummaryMessageLimit {
		return messages
	}
	if len(messages) <= chatBodySummaryMessageLimit*2 {
		return messages
	}
	out := make([]ChatMessage, 0, chatBodySummaryMessageLimit*2)
	out = append(out, messages[:chatBodySummaryMessageLimit]...)
	out = append(out, messages[len(messages)-chatBodySummaryMessageLimit:]...)
	return out
}

func summarizeMessage(msg ChatMessage) MsgSummary {
	content := FlattenContent(msg.Content)
	if len(content) > maxContentChars {
		content = content[:maxContentChars]
	}
	summary := MsgSummary{
		Role:         msg.Role,
		ContentChars: len(content),
	}
	if len(msg.ToolCalls) > 0 {
		summary.HasToolCalls = true
		summary.ToolCallCount = len(msg.ToolCalls)
		summary.ToolCallID = msg.ToolCalls[0].ID
	}
	return summary
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
