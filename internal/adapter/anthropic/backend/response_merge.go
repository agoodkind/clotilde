package anthropicbackend

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

// JSONCoercion captures the optional JSON-coercion contract a caller
// may provide when merging the buffered Anthropic stream chunks into a
// final OpenAI ChatResponse. The Anthropic backend does not know about
// the root-side JSONResponseSpec type, so callers translate to this
// neutral form (or nil-coercion when the caller did not request
// structured output).
type JSONCoercion struct {
	// Coerce returns a JSON-safe variant of the buffered assistant
	// text. Set to nil when the caller did not request structured
	// output. Coerce should not mutate the input.
	Coerce func(text string) string
	// Validate reports whether the coerced text is valid JSON. The
	// merger only swaps in the coerced text when Validate returns
	// true. Set to nil when no validation is required.
	Validate func(text string) bool
}

// ResponseFormatSpec is the backend-local shape for the optional OpenAI
// `response_format` request contract. The root adapter translates its decoded
// request field into this neutral backend form so Anthropic ownership stays
// inside the backend package without creating a package cycle back to root.
type ResponseFormatSpec struct {
	Mode       string
	SchemaName string
	// TODO replace with a deeply enumerated named type
	Schema     json.RawMessage
}

// MergeStreamChunks reconstructs the final ChatResponse from a buffer
// of streamed OpenAI chunks emitted by the Anthropic translator.
//
// The merger reassembles assistant text, reasoning (preferring
// `delta.reasoning`, falling back to `delta.reasoning_content`), refusal
// text, and tool-call deltas into the OpenAI non-streaming response
// shape. Anthropic's distinct `stop_reason == "refusal"` is mapped onto
// `message.refusal` when no explicit refusal delta arrived.
//
// JSON coercion is opt-in: callers that requested a structured
// `response_format` pass a JSONCoercion with both Coerce and Validate
// set; otherwise the merger leaves assistant text untouched.
func MergeStreamChunks(
	reqID, modelAlias, systemFingerprint string,
	chunks []adapteropenai.StreamChunk,
	usage adapteropenai.Usage,
	finishReason string,
	json JSONCoercion,
	anthropicStopReason string,
) adapteropenai.ChatResponse {
	var text strings.Builder
	var reasoning strings.Builder
	var refusalText strings.Builder
	type toolSlot struct {
		id   string
		typ  string
		name string
		args string
	}
	toolAcc := make(map[int]*toolSlot)
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
				slot = &toolSlot{}
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
	if json.Coerce != nil {
		coerced := json.Coerce(outText)
		if json.Validate == nil || json.Validate(coerced) {
			outText = coerced
		}
	}

	msg := adapteropenai.ChatMessage{
		Role:    "assistant",
		Content: jsonRawQuoted(outText),
	}
	if reasoning.Len() > 0 {
		msg.Reasoning = reasoning.String()
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
		msg.ToolCalls = append(msg.ToolCalls, adapteropenai.ToolCall{
			Index: i,
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
			FinishReason: finishReason,
		}},
		Usage: &usage,
	}
}

func jsonRawQuoted(s string) json.RawMessage {
	return json.RawMessage(strconv.Quote(s))
}
