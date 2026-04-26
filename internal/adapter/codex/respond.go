package codex

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type toolAccumulator struct {
	id   string
	typ  string
	name string
	args string
}

func MergeChunks(reqID, modelAlias, systemFingerprint string, chunks []adapteropenai.StreamChunk, res RunResult) adapteropenai.ChatResponse {
	var text strings.Builder
	var reasoning strings.Builder
	var refusalText strings.Builder
	toolAcc := make(map[int]*toolAccumulator)
	for _, ch := range chunks {
		if len(ch.Choices) == 0 {
			continue
		}
		delta := ch.Choices[0].Delta
		text.WriteString(delta.Content)
		if delta.Reasoning != "" {
			reasoning.WriteString(delta.Reasoning)
		} else {
			reasoning.WriteString(delta.ReasoningContent)
		}
		refusalText.WriteString(delta.Refusal)
		for _, tc := range delta.ToolCalls {
			slot := toolAcc[tc.Index]
			if slot == nil {
				slot = &toolAccumulator{}
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

	msg := adapteropenai.ChatMessage{
		Role:    "assistant",
		Content: json.RawMessage(strconv.Quote(text.String())),
	}
	if reasoning.Len() > 0 {
		msg.Reasoning = reasoning.String()
		msg.ReasoningContent = reasoning.String()
	}
	if refusalText.Len() > 0 {
		msg.Refusal = refusalText.String()
	}
	var order []int
	for idx := range toolAcc {
		order = append(order, idx)
	}
	sort.Ints(order)
	for _, idx := range order {
		slot := toolAcc[idx]
		typ := slot.typ
		if typ == "" {
			typ = "function"
		}
		msg.ToolCalls = append(msg.ToolCalls, adapteropenai.ToolCall{
			Index: idx,
			ID:    slot.id,
			Type:  typ,
			Function: adapteropenai.ToolCallFunction{
				Name:      slot.name,
				Arguments: slot.args,
			},
		})
	}

	return adapteropenai.ChatResponse{
		ID:                reqID,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             modelAlias,
		SystemFingerprint: systemFingerprint,
		Choices: []adapteropenai.ChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: res.FinishReason,
		}},
		Usage: &res.Usage,
	}
}
