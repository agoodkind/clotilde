package tooltrans

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/finishreason"
)

// StreamTranslator converts Anthropic SSE events into OpenAI streaming chunks.
type StreamTranslator struct {
	createdUnix        int64
	modelAlias         string
	reqID              string
	blockIndex         int
	currentBlockType   string
	toolCallIndex      int
	toolCallByBlockIdx map[int]int
	seenRole           bool
	pendingInputTokens int
	lastStopReason     string
	lastOutputTokens   int
	visibleText        strings.Builder

	// thinkingOpen tracks whether we've already written the opening
	// <think> tag into the content stream for the current Anthropic
	// thinking block. Subsequent thinking_delta events within the same
	// block append without re-opening; the closing </think> is emitted
	// on the matching content_block_stop.
	thinkingOpen bool
}

// NewStreamTranslator builds per-request stream state.
func NewStreamTranslator(reqID, modelAlias string) *StreamTranslator {
	return &StreamTranslator{
		createdUnix:        time.Now().Unix(),
		modelAlias:         modelAlias,
		reqID:              reqID,
		toolCallByBlockIdx: make(map[int]int),
	}
}

// HandleEvent maps one Anthropic stream event to zero or more OpenAI chunks.
func (t *StreamTranslator) HandleEvent(eventName string, dataJSON []byte) (
	chunks []OpenAIStreamChunk,
	finished bool,
	finishReason string,
	usage *OpenAIUsage,
	err error,
) {
	evName := strings.TrimSpace(eventName)
	if evName == "" {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(dataJSON, &probe); err == nil && probe.Type != "" {
			evName = probe.Type
		}
	}

	switch evName {
	case "message_start":
		var ev struct {
			Message struct {
				Usage *AnthSSEUsage `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(dataJSON, &ev); err != nil {
			return nil, false, "", nil, err
		}
		if ev.Message.Usage != nil {
			t.pendingInputTokens = ev.Message.Usage.InputTokens
		}
		return nil, false, "", nil, nil

	case "content_block_start":
		var ev struct {
			Index        int               `json:"index"`
			ContentBlock *AnthContentBlock `json:"content_block"`
		}
		if err := json.Unmarshal(dataJSON, &ev); err != nil {
			return nil, false, "", nil, err
		}
		if ev.ContentBlock == nil {
			return nil, false, "", nil, nil
		}
		switch ev.ContentBlock.Type {
		case "text":
			t.currentBlockType = "text"
		case "tool_use":
			t.currentBlockType = "tool_use"
			idx := t.toolCallIndex
			t.toolCallIndex++
			t.toolCallByBlockIdx[ev.Index] = idx
			ch := t.baseChunk(OpenAIStreamDelta{
				ToolCalls: []OpenAIToolCall{{
					Index: idx,
					ID:    ev.ContentBlock.ID,
					Type:  "function",
					Function: OpenAIToolCallFunction{
						Name:      ev.ContentBlock.Name,
						Arguments: "",
					},
				}},
			})
			return []OpenAIStreamChunk{ch}, false, "", nil, nil
		case "thinking":
			t.currentBlockType = "thinking"
			if !t.seenRole {
				t.seenRole = true
				ch := t.baseChunk(OpenAIStreamDelta{Role: "assistant"})
				return []OpenAIStreamChunk{ch}, false, "", nil, nil
			}
		default:
			t.currentBlockType = ev.ContentBlock.Type
		}
		return nil, false, "", nil, nil

	case "content_block_delta":
		var ev struct {
			Index int           `json:"index"`
			Delta *AnthSSEDelta `json:"delta"`
		}
		if err := json.Unmarshal(dataJSON, &ev); err != nil {
			return nil, false, "", nil, err
		}
		if ev.Delta == nil {
			return nil, false, "", nil, nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			delta := OpenAIStreamDelta{Content: ev.Delta.Text}
			t.visibleText.WriteString(ev.Delta.Text)
			if !t.seenRole {
				delta.Role = "assistant"
				t.seenRole = true
			}
			ch := t.baseChunk(delta)
			return []OpenAIStreamChunk{ch}, false, "", nil, nil
		case "input_json_delta":
			tcIdx, ok := t.toolCallByBlockIdx[ev.Index]
			if !ok {
				return nil, false, "", nil, fmt.Errorf("unknown tool block index %d", ev.Index)
			}
			ch := t.baseChunk(OpenAIStreamDelta{
				ToolCalls: []OpenAIToolCall{{
					Index: tcIdx,
					Type:  "function",
					Function: OpenAIToolCallFunction{
						Arguments: ev.Delta.PartialJSON,
					},
				}},
			})
			return []OpenAIStreamChunk{ch}, false, "", nil, nil
		case "thinking_delta":
			// Cursor BYOK 3.2 does not parse any reasoning field on
			// stream deltas (the installed workbench only reads
			// delta.content and delta.tool_calls). Emitting thinking
			// as delta.reasoning / delta.reasoning_content buys us
			// nothing for the observed consumer today. Collapsibles
			// (<details>) block progressive render because the
			// markdown renderer waits for the closing tag. Plain
			// blockquote content renders line by line as it streams,
			// which is the shape that already worked in Cursor.
			//
			// Sentinel HTML comments bookend the block so
			// stripThinkingBlockquote in openai_to_anthropic.go can
			// remove the whole envelope on the return trip, keeping
			// the Anthropic cached prefix byte-stable across turns.
			text := ev.Delta.Thinking
			contentOut := FormatThinkingInlineDelta(!t.thinkingOpen, text)
			if !t.thinkingOpen {
				t.thinkingOpen = true
			}
			delta := OpenAIStreamDelta{
				Content: contentOut,
			}
			if !t.seenRole {
				delta.Role = "assistant"
				t.seenRole = true
			}
			t.visibleText.WriteString(contentOut)
			ch := t.baseChunk(delta)
			return []OpenAIStreamChunk{ch}, false, "", nil, nil
		default:
			return nil, false, "", nil, nil
		}

	case "content_block_stop":
		// When an Anthropic thinking block ends emit the closing
		// sentinel comment plus a blank line so the blockquote
		// terminates cleanly before the visible answer streams.
		// stripThinkingBlockquote on the return trip matches the
		// full envelope including the trailing whitespace so the
		// cached prefix stays byte-stable across turns.
		if t.currentBlockType == "thinking" && t.thinkingOpen {
			t.currentBlockType = ""
			t.thinkingOpen = false
			return []OpenAIStreamChunk{t.baseChunk(OpenAIStreamDelta{
				Content: ThinkingInlineClose(),
			})}, false, "", nil, nil
		}
		t.currentBlockType = ""
		return nil, false, "", nil, nil

	case "message_delta":
		var ev struct {
			Delta struct {
				StopReason   string `json:"stop_reason"`
				StopSequence string `json:"stop_sequence"`
			} `json:"delta"`
			Usage *AnthSSEUsage `json:"usage"`
		}
		if err := json.Unmarshal(dataJSON, &ev); err != nil {
			return nil, false, "", nil, err
		}
		if ev.Delta.StopReason != "" {
			t.lastStopReason = ev.Delta.StopReason
		}
		if ev.Usage != nil {
			t.lastOutputTokens = ev.Usage.OutputTokens
		}
		return nil, false, "", nil, nil

	case "message_stop":
		reason := finishreason.FromAnthropicStream(t.lastStopReason)
		u := &OpenAIUsage{
			PromptTokens:     t.pendingInputTokens,
			CompletionTokens: t.lastOutputTokens,
			TotalTokens:      t.pendingInputTokens + t.lastOutputTokens,
		}
		var extra []OpenAIStreamChunk
		if t.lastStopReason == "refusal" && t.visibleText.Len() > 0 {
			extra = append(extra, t.baseChunk(OpenAIStreamDelta{Refusal: t.visibleText.String()}))
		}
		return extra, true, reason, u, nil

	case "ping":
		return nil, false, "", nil, nil

	case "error":
		var ev struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(dataJSON, &ev); err != nil {
			return nil, false, "", nil, err
		}
		msg := strings.TrimSpace(ev.Error.Message)
		if msg == "" {
			msg = "anthropic stream error"
		}
		return nil, false, "", nil, fmt.Errorf("%s", msg)

	default:
		return nil, false, "", nil, nil
	}
}

func (t *StreamTranslator) baseChunk(delta OpenAIStreamDelta) OpenAIStreamChunk {
	return OpenAIStreamChunk{
		ID:      t.reqID,
		Object:  "chat.completion.chunk",
		Created: t.createdUnix,
		Model:   t.modelAlias,
		Choices: []OpenAIStreamChoice{{
			Index: 0,
			Delta: delta,
		}},
	}
}
