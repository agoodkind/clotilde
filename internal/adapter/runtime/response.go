package runtime

import (
	"encoding/json"
	"time"
)

type AssistantMessageParts struct {
	Text             string
	ReasoningContent string
	Refusal          string
	ToolCalls        []ToolCall
}

func BuildAssistantMessage(parts AssistantMessageParts) ChatMessage {
	msg := ChatMessage{Role: "assistant"}
	if parts.ReasoningContent != "" {
		msg.Reasoning = parts.ReasoningContent
		msg.ReasoningContent = parts.ReasoningContent
	}
	switch {
	case parts.Refusal != "":
		msg.Refusal = parts.Refusal
		msg.Content = json.RawMessage("null")
	case len(parts.ToolCalls) > 0:
		msg.ToolCalls = parts.ToolCalls
		if parts.Text == "" {
			msg.Content = json.RawMessage("null")
		} else {
			msg.Content = encodeJSONString(parts.Text)
		}
	default:
		msg.Content = encodeJSONString(parts.Text)
	}
	return msg
}

func BuildChatCompletion(reqID, modelAlias, systemFingerprint string, msg ChatMessage, finishReason string, usage Usage) ChatResponse {
	return ChatResponse{
		ID:                reqID,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             modelAlias,
		SystemFingerprint: systemFingerprint,
		Choices: []ChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
		}},
		Usage: &usage,
	}
}

func BuildDeltaChunk(reqID, modelAlias string, created int64, delta StreamDelta) StreamChunk {
	return StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelAlias,
		Choices: []StreamChoice{{
			Index: 0,
			Delta: delta,
		}},
	}
}

func EmitDeltaChunk(emit func(StreamChunk) error, reqID, modelAlias string, created int64, delta StreamDelta) error {
	return emit(BuildDeltaChunk(reqID, modelAlias, created, delta))
}

func encodeJSONString(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return b
}
