package tooltrans

import (
	"encoding/json"
	"fmt"
	"log/slog"
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
			slog.Debug("tooltrans.thinking.dropped", "block_index", ev.Index)
			t.currentBlockType = "thinking"
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
			slog.Debug("tooltrans.thinking.delta_dropped", "block_index", ev.Index)
			return nil, false, "", nil, nil
		default:
			return nil, false, "", nil, nil
		}

	case "content_block_stop":
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
		return nil, true, reason, u, nil

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
