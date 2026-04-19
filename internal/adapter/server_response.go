package adapter

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type toolAccSlot struct {
	id, typ, name, args string
}

func mergeOAuthStreamChunks(reqID, modelAlias string, chunks []tooltrans.OpenAIStreamChunk, u Usage, finishReason string, jsonSpec JSONResponseSpec, anthropicStopReason string) ChatResponse {
	var text strings.Builder
	var reasoning strings.Builder
	var refusalText strings.Builder
	toolAcc := make(map[int]*toolAccSlot)
	for _, ch := range chunks {
		if len(ch.Choices) == 0 {
			continue
		}
		delta := ch.Choices[0].Delta
		text.WriteString(delta.Content)
		reasoning.WriteString(delta.ReasoningContent)
		refusalText.WriteString(delta.Refusal)
		for _, tc := range delta.ToolCalls {
			slot := toolAcc[tc.Index]
			if slot == nil {
				slot = &toolAccSlot{}
				toolAcc[tc.Index] = slot
			}
			if tc.ID != "" {
				slot.id = tc.ID
			}
			if tc.Type != "" {
				slot.typ = tc.Type
			}
			if tc.Function.Name != "" {
				slot.name = tc.Function.Name
			}
			slot.args += tc.Function.Arguments
		}
	}

	outText := text.String()
	if jsonSpec.Mode != "" {
		coerced := CoerceJSON(outText)
		if LooksLikeJSON(coerced) {
			outText = coerced
		}
	}

	msg := ChatMessage{
		Role:    "assistant",
		Content: json.RawMessage(strconv.Quote(outText)),
	}
	if reasoning.Len() > 0 {
		msg.ReasoningContent = reasoning.String()
	}
	if refusalText.Len() > 0 {
		msg.Refusal = refusalText.String()
	} else if strings.EqualFold(anthropicStopReason, "refusal") && strings.TrimSpace(outText) != "" {
		msg.Refusal = outText
	}
	var order []int
	for k := range toolAcc {
		order = append(order, k)
	}
	sort.Ints(order)
	for _, i := range order {
		slot := toolAcc[i]
		typ := slot.typ
		if typ == "" {
			typ = "function"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			Index: i,
			ID:    slot.id,
			Type:  typ,
			Function: ToolCallFunction{
				Name:      slot.name,
				Arguments: slot.args,
			},
		})
	}

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
		Usage: &u,
	}
}
