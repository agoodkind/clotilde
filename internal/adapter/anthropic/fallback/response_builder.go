package fallback

import (
	"strings"

	"goodkind.io/clyde/internal/adapter/finishreason"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

// TextCoercer optionally rewrites assistant text before it is placed on
// the OpenAI-compatible response. The root adapter uses this for JSON
// response-format coercion.
type TextCoercer func(string) (string, bool)

type FinalResponseInput struct {
	Request           Request
	Result            Result
	RequestID         string
	ModelAlias        string
	SystemFingerprint string
	CoerceText        TextCoercer
}

type FinalResponse struct {
	Response     adapteropenai.ChatResponse
	Usage        adapteropenai.Usage
	FinishReason string
}

func BuildFinalResponse(in FinalResponseInput) FinalResponse {
	finalText := in.Result.Text
	if in.CoerceText != nil && in.Result.Refusal == "" {
		if coerced, ok := in.CoerceText(in.Result.Text); ok {
			finalText = coerced
		}
	}
	usage := OpenAIUsage(in.Result.Usage)
	msg := adapterruntime.BuildAssistantMessage(adapterruntime.AssistantMessageParts{
		Text:             finalText,
		ReasoningContent: in.Result.ReasoningContent,
		Refusal:          in.Result.Refusal,
		ToolCalls:        OpenAIToolCalls(in.Result.ToolCalls, in.RequestID),
	})
	finish := finishreason.FromAnthropicNonStream(in.Result.Stop)
	return FinalResponse{
		Response:     adapterruntime.BuildChatCompletion(in.RequestID, in.ModelAlias, in.SystemFingerprint, msg, finish, usage),
		Usage:        usage,
		FinishReason: finish,
	}
}

type StreamPlanInput struct {
	Request     Request
	Result      StreamResult
	RequestID   string
	ModelAlias  string
	Created     int64
	BufferedRun bool
}

type StreamPlan struct {
	Chunks       []adapteropenai.StreamChunk
	Usage        adapteropenai.Usage
	FinishReason string
}

func BuildStreamPlan(in StreamPlanInput) StreamPlan {
	finish := finishreason.FromAnthropicNonStream(in.Result.Stop)
	var chunks []adapteropenai.StreamChunk
	if in.BufferedRun {
		if len(in.Result.ToolCalls) > 0 {
			rc := strings.TrimSpace(in.Result.ReasoningContent)
			delta := adapteropenai.StreamDelta{Role: "assistant"}
			if rc != "" {
				delta.Reasoning = rc
				delta.ReasoningContent = rc
			}
			chunks = append(chunks, adapterruntime.BuildDeltaChunk(in.RequestID, in.ModelAlias, in.Created, delta))
			for i, tc := range in.Result.ToolCalls {
				chunks = append(chunks, adapterruntime.BuildDeltaChunk(in.RequestID, in.ModelAlias, in.Created, adapteropenai.StreamDelta{
					ToolCalls: OpenAIToolCalls([]ToolCall{tc}, in.RequestID, i),
				}))
			}
			finish = "tool_calls"
		} else {
			delta := adapteropenai.StreamDelta{Role: "assistant", Content: in.Result.Text}
			if rc := strings.TrimSpace(in.Result.ReasoningContent); rc != "" {
				delta.Reasoning = rc
				delta.ReasoningContent = rc
			}
			chunks = append(chunks, adapterruntime.BuildDeltaChunk(in.RequestID, in.ModelAlias, in.Created, delta))
		}
	}
	return StreamPlan{
		Chunks:       chunks,
		Usage:        OpenAIUsage(in.Result.Usage),
		FinishReason: finish,
	}
}

func BuildLiveStreamChunk(reqID, modelAlias string, created int64, ev StreamEvent, includeRole bool) (adapteropenai.StreamChunk, bool) {
	delta := adapteropenai.StreamDelta{}
	switch ev.Kind {
	case "text":
		delta.Content = ev.Text
	case "reasoning":
		delta.Reasoning = ev.Text
		delta.ReasoningContent = ev.Text
	default:
		return adapteropenai.StreamChunk{}, false
	}
	if includeRole {
		delta.Role = "assistant"
	}
	return adapterruntime.BuildDeltaChunk(reqID, modelAlias, created, delta), true
}

func OpenAIUsage(u Usage) adapteropenai.Usage {
	out := adapteropenai.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.CacheReadInputTokens > 0 {
		out.PromptTokensDetails = &adapteropenai.PromptTokensDetails{CachedTokens: u.CacheReadInputTokens}
	}
	return out
}

func OpenAIToolCalls(calls []ToolCall, reqID string, indexOffset ...int) []adapterruntime.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	offset := 0
	if len(indexOffset) > 0 {
		offset = indexOffset[0]
	}
	out := make([]adapterruntime.ToolCall, len(calls))
	for i, tc := range calls {
		index := i + offset
		out[i] = adapterruntime.ToolCall{
			Index: index,
			ID:    EnsureToolCallID(tc.ID, reqID, index),
			Type:  "function",
			Function: adapterruntime.ToolCallFunction{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		}
	}
	return out
}

func ShouldBufferTools(req Request) bool {
	return len(req.Tools) > 0 && strings.ToLower(strings.TrimSpace(req.ToolChoice)) != "none"
}

// PathLabel picks the dispatch tag for log events based on whether the
// request rides the synthesized-transcript resume pathway.
func PathLabel(req Request) string {
	if req.Resume {
		return "fallback_resume"
	}
	return "fallback_prompt"
}
