package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"goodkind.io/clyde/internal/adapter/finishreason"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
)

type Reasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type RunResult struct {
	Usage                      adapteropenai.Usage
	FinishReason               string
	ReasoningSignaled          bool
	ReasoningVisible           bool
	DerivedCacheCreationTokens int
	ResponseID                 string
	OutputItems                []map[string]any
	ToolCallCount              int
	HasSubagentToolCall        bool
}

// ContextWindowError reports an upstream Codex over-context rejection.
// The adapter maps this to OpenAI's context_length_exceeded shape so
// Cursor can run its normal compaction/retry flow.
type ContextWindowError struct {
	Message string
}

func (e *ContextWindowError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "codex input exceeds context window"
	}
	return e.Message
}

func NewRunResult(finishReason string) RunResult {
	return RunResult{FinishReason: finishreason.FromCodex(finishReason)}
}

func (r *RunResult) SetFinishReason(finishReason string) {
	r.FinishReason = finishreason.FromCodex(finishReason)
}

type completedResponse struct {
	Response struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			TotalTokens        int `json:"total_tokens"`
			InputTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

// Mirrors the observed Responses SSE envelope from
// research/codex/codex-rs/codex-api/src/sse/responses.rs and the local
// mock websocket script. The item payload remains a named raw-object boundary
// because Codex emits a broad response-item union here.
type transportStreamEvent struct {
	Type         string              `json:"type"`
	Response     *transportResponse  `json:"response,omitempty"`
	Item         transportItem       `json:"item,omitempty"`
	ItemID       string              `json:"item_id,omitempty"`
	CallID       string              `json:"call_id,omitempty"`
	Delta        string              `json:"delta,omitempty"`
	SummaryIndex *int                `json:"summary_index,omitempty"`
	Error        *transportErrorBody `json:"error,omitempty"`
}

type transportResponse struct {
	ID                string `json:"id,omitempty"`
	Status            string `json:"status,omitempty"`
	IncompleteDetails struct {
		Reason string `json:"reason,omitempty"`
	} `json:"incomplete_details"`
	Usage struct {
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens,omitempty"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

type transportErrorBody struct {
	Message string `json:"message,omitempty"`
}

type transportItem map[string]json.RawMessage

type toolCallState struct {
	Index             int
	ItemID            string
	CallID            string
	Name              string
	NativeName        string
	Type              string
	IdentityEmitted   bool
	ArgumentDeltaSeen bool
	ArgumentsEmitted  bool
	Arguments         strings.Builder
	Input             strings.Builder
}

func SanitizeForUpstreamCache(text string) string {
	text = StripNoticeSentinel(text)
	text = StripActivitySentinel(text)
	text = StripThinkingSentinel(text)
	return text
}

// ClientMetadataWithTurn extends ClientMetadata with the
// `x-codex-turn-metadata` JSON blob. Codex CLI and Codex Desktop both
// mirror the handshake header into client_metadata; we do the same.
// turnMetadataJSON should be the already-marshaled JSON string from
// TurnMetadata.MarshalCompact.
func ClientMetadataWithTurn(installationID, windowID, turnMetadataJSON string) map[string]string {
	out := map[string]string{}
	if v := strings.TrimSpace(installationID); v != "" {
		out["x-codex-installation-id"] = v
	}
	if v := strings.TrimSpace(windowID); v != "" {
		out["x-codex-window-id"] = v
	}
	if v := strings.TrimSpace(turnMetadataJSON); v != "" {
		out["x-codex-turn-metadata"] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func RequestInclude(requested []string, reasoningEnabled bool) []string {
	if len(requested) == 0 && !reasoningEnabled {
		return nil
	}
	seen := make(map[string]struct{}, len(requested)+1)
	out := make([]string, 0, len(requested)+1)
	for _, item := range requested {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	if reasoningEnabled {
		const encryptedReasoning = "reasoning.encrypted_content"
		if _, ok := seen[encryptedReasoning]; !ok {
			out = append(out, encryptedReasoning)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func EffectiveReasoning(req adapteropenai.ChatRequest, effort string) *Reasoning {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		effort = strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	}
	if effort == "" && req.Reasoning != nil {
		effort = strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
	}
	var out Reasoning
	switch effort {
	case "low", "medium", "high", "xhigh":
		out.Effort = effort
	}
	if req.Reasoning != nil {
		switch strings.ToLower(strings.TrimSpace(req.Reasoning.Summary)) {
		case "auto", "detailed", "none":
			out.Summary = strings.ToLower(strings.TrimSpace(req.Reasoning.Summary))
		}
	}
	if out.Effort == "" && out.Summary == "" {
		return nil
	}
	return &out
}

func ParseSSEEvents(body io.Reader, emit func(adapterrender.Event) error) (RunResult, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 1024*128), 1024*1024*8)

	var eventName string
	var dataLines []string
	out := NewRunResult("stop")
	reasoningSignaled := false
	reasoningVisible := false
	toolCallsByItemID := make(map[string]*toolCallState)
	nextToolIndex := 0
	emitToolCall := func(state *toolCallState, fn adapteropenai.ToolCallFunction) error {
		if state == nil {
			return nil
		}
		tc := adapteropenai.ToolCall{
			Index:    state.Index,
			Function: fn,
		}
		if !state.IdentityEmitted {
			tc.ID = state.CallID
			tc.Type = state.Type
			state.IdentityEmitted = true
		}
		return emit(adapterrender.Event{
			Kind:      adapterrender.EventToolCallDelta,
			ToolCalls: []adapteropenai.ToolCall{tc},
		})
	}
	getToolState := func(itemID, callID, name string) (*toolCallState, bool) {
		itemID = strings.TrimSpace(itemID)
		callID = strings.TrimSpace(callID)
		if itemID == "" {
			itemID = callID
		}
		if callID == "" {
			callID = itemID
		}
		if state := toolCallsByItemID[itemID]; state != nil {
			if state.CallID == "" {
				state.CallID = callID
			}
			if state.Name == "" {
				state.Name = name
			}
			if callID != "" {
				toolCallsByItemID[callID] = state
			}
			return state, false
		}
		if callID != "" {
			if state := toolCallsByItemID[callID]; state != nil {
				if itemID != "" {
					toolCallsByItemID[itemID] = state
				}
				if state.Name == "" {
					state.Name = name
				}
				return state, false
			}
		}
		state := &toolCallState{
			Index:  nextToolIndex,
			ItemID: itemID,
			CallID: callID,
			Name:   name,
			Type:   "function",
		}
		nextToolIndex++
		out.ToolCallCount = max(out.ToolCallCount, nextToolIndex)
		if name == "Subagent" || name == "spawn_agent" {
			out.HasSubagentToolCall = true
		}
		if itemID != "" {
			toolCallsByItemID[itemID] = state
		}
		if callID != "" {
			toolCallsByItemID[callID] = state
		}
		return state, true
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			if eventName == "" || len(dataLines) == 0 {
				eventName = ""
				dataLines = nil
				continue
			}
			payload := strings.Join(dataLines, "\n")
			eventNameLocal := eventName
			eventName = ""
			dataLines = nil

			if strings.TrimSpace(payload) == "[DONE]" {
				break
			}
			var raw transportStreamEvent
			if err := json.Unmarshal([]byte(payload), &raw); err != nil {
				continue
			}

			if eventNameLocal == "response.output_text.delta" {
				if delta := raw.Delta; delta != "" {
					if err := emit(adapterrender.Event{Kind: adapterrender.EventAssistantTextDelta, Text: delta}); err != nil {
						return out, err
					}
				}
				continue
			}

			if eventNameLocal == "response.output_item.added" || eventNameLocal == "response.output_item.done" {
				item := raw.Item
				itemType := item.string("type")
				switch itemType {
				case "function_call":
					itemID := strings.TrimSpace(item.string("id"))
					callID := strings.TrimSpace(item.string("call_id"))
					if itemID == "" {
						itemID = callID
					}
					if callID == "" {
						callID = itemID
					}
					name := strings.TrimSpace(item.string("name"))
					args := item.string("arguments")
					cursorName := InboundToolName(name)
					state, created := getToolState(itemID, callID, cursorName)
					if state.NativeName == "" {
						state.NativeName = name
					}
					if created {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Name: state.Name}); err != nil {
							return out, err
						}
					}
					if state.Name == "" && name != "" {
						state.Name = InboundToolName(name)
					}
					if state.Name == "Subagent" || state.NativeName == "spawn_agent" {
						out.HasSubagentToolCall = true
					}
					if eventNameLocal == "response.output_item.done" && item != nil {
						completed := item.cloneMap()
						if strings.TrimSpace(mapString(completed, "arguments")) == "" && state.Arguments.Len() > 0 {
							completed["arguments"] = state.Arguments.String()
						}
						out.OutputItems = append(out.OutputItems, completed)
					}
					out.SetFinishReason("tool_calls")
					if eventNameLocal == "response.output_item.done" && state.NativeName == "shell_command" {
						if args == "" {
							args = state.Arguments.String()
						}
						if converted, ok := ShellArgsFromShellCommandArguments(args); ok && !state.ArgumentsEmitted {
							if err := emitToolCall(state, adapteropenai.ToolCallFunction{Arguments: converted}); err != nil {
								return out, err
							}
							state.ArgumentsEmitted = true
						} else if !state.ArgumentsEmitted {
							LogToolingEvent(nil, context.Background(), "", "shell_command.parse_failed",
								slog.String("item_type", itemType),
								slog.String("item_id", itemID),
								slog.String("tool_name", "Shell"),
							)
						}
						continue
					}
					if eventNameLocal == "response.output_item.done" && args != "" && !state.ArgumentDeltaSeen {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Arguments: args}); err != nil {
							return out, err
						}
						state.ArgumentsEmitted = true
					}
				case "local_shell_call":
					if eventNameLocal == "response.output_item.done" && item != nil {
						out.OutputItems = append(out.OutputItems, item.cloneMap())
					}
					itemID := strings.TrimSpace(item.string("id"))
					callID := strings.TrimSpace(item.string("call_id"))
					state, created := getToolState(itemID, callID, "Shell")
					if created {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Name: "Shell"}); err != nil {
							return out, err
						}
					}
					out.SetFinishReason("tool_calls")
					if args, ok := ShellArgsFromLocalShellItem(item.cloneMap()); ok && !state.ArgumentsEmitted {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Arguments: args}); err != nil {
							return out, err
						}
						state.ArgumentsEmitted = true
					} else if eventNameLocal == "response.output_item.done" && !state.ArgumentsEmitted {
						LogToolingEvent(nil, context.Background(), "", "native_local_shell.parse_failed",
							slog.String("item_type", itemType),
							slog.String("item_id", itemID),
							slog.String("tool_name", "Shell"),
						)
					}
				case "custom_tool_call":
					if eventNameLocal == "response.output_item.done" && item != nil {
						out.OutputItems = append(out.OutputItems, item.cloneMap())
					}
					itemID := strings.TrimSpace(item.string("id"))
					callID := strings.TrimSpace(item.string("call_id"))
					name := item.string("name")
					cursorName := InboundToolName(name)
					if IsApplyPatchToolName(cursorName) || IsApplyPatchToolName(name) {
						cursorName = "ApplyPatch"
					}
					state, created := getToolState(itemID, callID, cursorName)
					if state.Name == "Subagent" || name == "spawn_agent" {
						out.HasSubagentToolCall = true
					}
					if created {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Name: state.Name}); err != nil {
							return out, err
						}
					}
					out.SetFinishReason("tool_calls")
					input := item.string("input")
					if input == "" {
						input = state.Input.String()
					}
					if args, ok := ApplyPatchArgs(input); ok && !state.ArgumentsEmitted {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Arguments: args}); err != nil {
							return out, err
						}
						state.ArgumentsEmitted = true
					} else if eventNameLocal == "response.output_item.done" && !state.ArgumentsEmitted {
						LogToolingEvent(nil, context.Background(), "", "native_custom_tool.parse_failed",
							slog.String("item_type", itemType),
							slog.String("item_id", itemID),
							slog.String("tool_name", cursorName),
						)
					}
				default:
					if eventNameLocal == "response.output_item.done" && item != nil {
						out.OutputItems = append(out.OutputItems, item.cloneMap())
					}
				}
				continue
			}

			if eventNameLocal == "response.function_call_arguments.delta" {
				itemID := strings.TrimSpace(raw.ItemID)
				delta := raw.Delta
				state := toolCallsByItemID[itemID]
				if state != nil && delta != "" {
					state.ArgumentDeltaSeen = true
					state.Arguments.WriteString(delta)
					out.SetFinishReason("tool_calls")
					if state.NativeName == "shell_command" {
						continue
					}
					if err := emitToolCall(state, adapteropenai.ToolCallFunction{Arguments: delta}); err != nil {
						return out, err
					}
				}
				continue
			}

			if eventNameLocal == "response.custom_tool_call_input.delta" {
				itemID := strings.TrimSpace(raw.ItemID)
				callID := strings.TrimSpace(raw.CallID)
				delta := raw.Delta
				state, created := getToolState(itemID, callID, "ApplyPatch")
				if created {
					if err := emitToolCall(state, adapteropenai.ToolCallFunction{Name: state.Name}); err != nil {
						return out, err
					}
				}
				if delta != "" {
					state.Input.WriteString(delta)
					state.ArgumentDeltaSeen = true
					out.SetFinishReason("tool_calls")
					if err := emitToolCall(state, adapteropenai.ToolCallFunction{Arguments: delta}); err != nil {
						return out, err
					}
					state.ArgumentsEmitted = true
				}
				continue
			}

			if strings.Contains(eventNameLocal, "reasoning") && strings.HasSuffix(eventNameLocal, ".delta") {
				if delta := raw.Delta; delta != "" {
					reasoningSignaled = true
					reasoningVisible = true
					kind := "text"
					var summaryIdx *int
					if strings.Contains(eventNameLocal, "summary") {
						kind = "summary"
						summaryIdx = raw.SummaryIndex
					}
					if err := emit(adapterrender.Event{
						Kind:          adapterrender.EventReasoningDelta,
						Text:          delta,
						ReasoningKind: kind,
						SummaryIndex:  summaryIdx,
					}); err != nil {
						return RunResult{}, err
					}
				}
				continue
			}

			if eventNameLocal == "response.completed" {
				var c completedResponse
				if err := json.Unmarshal([]byte(payload), &c); err == nil {
					if responseID := strings.TrimSpace(c.Response.ID); responseID != "" {
						out.ResponseID = responseID
					}
					out.Usage = mapUsage(c)
					if out.FinishReason != "tool_calls" {
						out.SetFinishReason(finishreason.FromCodexResponse(c.Response.Status, c.Response.IncompleteDetails.Reason))
					}
				}
				if reasoningTokensFromEvent(raw) > 0 {
					reasoningSignaled = true
				}
				if err := emit(adapterrender.Event{Kind: adapterrender.EventReasoningFinished}); err != nil {
					return out, err
				}
				out.ReasoningSignaled = reasoningSignaled
				out.ReasoningVisible = reasoningVisible
				return out, nil
			}
			if eventNameLocal == "response.created" {
				if raw.Response != nil {
					out.ResponseID = strings.TrimSpace(raw.Response.ID)
				}
				continue
			}
			if eventNameLocal == "response.failed" {
				_ = emit(adapterrender.Event{Kind: adapterrender.EventReasoningFinished})
				msg := "codex response failed"
				if raw.Error != nil && raw.Error.Message != "" {
					msg = raw.Error.Message
				}
				return out, codexResponseFailedError(msg)
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(value))
		}
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	out.ReasoningSignaled = reasoningSignaled
	out.ReasoningVisible = reasoningVisible
	return out, nil
}

func codexResponseFailedError(message string) error {
	if isCodexContextWindowMessage(message) {
		return &ContextWindowError{Message: strings.TrimSpace(message)}
	}
	return fmt.Errorf("%s", message)
}

func isCodexContextWindowMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(normalized, "exceeds the context window"):
		return true
	case strings.Contains(normalized, "context_length_exceeded"):
		return true
	case strings.Contains(normalized, "maximum context length"):
		return true
	default:
		return false
	}
}

func (item transportItem) string(key string) string {
	raw := item[key]
	if len(raw) == 0 {
		return ""
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	return out
}

func (item transportItem) cloneMap() map[string]any {
	if item == nil {
		return nil
	}
	raw, _ := json.Marshal(item)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func mapUsage(c completedResponse) adapteropenai.Usage {
	u := adapteropenai.Usage{
		PromptTokens:     c.Response.Usage.InputTokens,
		CompletionTokens: c.Response.Usage.OutputTokens,
		TotalTokens:      c.Response.Usage.TotalTokens,
	}
	if ct := c.Response.Usage.InputTokensDetails.CachedTokens; ct > 0 {
		u.PromptTokensDetails = &adapteropenai.PromptTokensDetails{CachedTokens: ct}
	}
	return u
}

func reasoningTokensFromEvent(raw transportStreamEvent) int {
	if raw.Response == nil {
		return 0
	}
	return raw.Response.Usage.OutputTokensDetails.ReasoningTokens
}

func rawString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}
