package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	"goodkind.io/clyde/internal/adapter/finishreason"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
	"goodkind.io/clyde/internal/adapter/tooltrans"
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

func InboundToolName(name string) string {
	return adaptercursor.InboundCodexToolName(name)
}

func SanitizeForUpstreamCache(text string) string {
	text = tooltrans.StripNoticeSentinel(text)
	text = tooltrans.StripActivitySentinel(text)
	text = tooltrans.StripThinkingSentinel(text)
	return text
}

func ClientMetadata(installationID, windowID string) map[string]string {
	out := map[string]string{}
	if v := strings.TrimSpace(installationID); v != "" {
		out["x-codex-installation-id"] = v
	}
	if v := strings.TrimSpace(windowID); v != "" {
		out["x-codex-window-id"] = v
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

func EffectiveAppEffort(req adapteropenai.ChatRequest) any {
	if r := EffectiveReasoning(req, ""); r != nil && r.Effort != "" {
		return r.Effort
	}
	return nil
}

func EffectiveAppSummary(req adapteropenai.ChatRequest) any {
	if r := EffectiveReasoning(req, ""); r != nil && r.Summary != "" {
		return r.Summary
	}
	return nil
}

func ParseSSE(body io.Reader, renderer *adapterrender.EventRenderer, emit func(adapteropenai.StreamChunk) error) (RunResult, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 1024*128), 1024*1024*8)

	var eventName string
	var dataLines []string
	out := NewRunResult("stop")
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
		return EmitRendered(renderer, adapterrender.Event{
			Kind: adapterrender.EventToolCallDelta,
			ToolCalls: []adapteropenai.ToolCall{tc},
		}, emit, nil)
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
			var raw map[string]any
			if err := json.Unmarshal([]byte(payload), &raw); err != nil {
				continue
			}

			if eventNameLocal == "response.output_text.delta" {
				if delta, _ := raw["delta"].(string); delta != "" {
					if err := EmitRendered(renderer, adapterrender.Event{Kind: adapterrender.EventAssistantTextDelta, Text: delta}, emit, nil); err != nil {
						return out, err
					}
				}
				continue
			}

			if eventNameLocal == "response.output_item.added" || eventNameLocal == "response.output_item.done" {
				item, _ := raw["item"].(map[string]any)
				itemType, _ := item["type"].(string)
				if eventNameLocal == "response.output_item.done" && item != nil {
					out.OutputItems = append(out.OutputItems, cloneMap(item))
				}
				if itemType == "function_call" {
					itemID := strings.TrimSpace(mapString(item, "id"))
					callID := strings.TrimSpace(mapString(item, "call_id"))
					if itemID == "" {
						itemID = callID
					}
					if callID == "" {
						callID = itemID
					}
					name := strings.TrimSpace(mapString(item, "name"))
					args := mapString(item, "arguments")
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
				} else if itemType == "local_shell_call" {
					itemID := strings.TrimSpace(mapString(item, "id"))
					callID := strings.TrimSpace(mapString(item, "call_id"))
					state, created := getToolState(itemID, callID, "Shell")
					if created {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Name: "Shell"}); err != nil {
							return out, err
						}
					}
					out.SetFinishReason("tool_calls")
					if args, ok := ShellArgsFromLocalShellItem(item); ok && !state.ArgumentsEmitted {
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
				} else if itemType == "custom_tool_call" {
					itemID := strings.TrimSpace(mapString(item, "id"))
					callID := strings.TrimSpace(mapString(item, "call_id"))
					name := mapString(item, "name")
					cursorName := InboundToolName(name)
					if IsApplyPatchToolName(cursorName) || IsApplyPatchToolName(name) {
						cursorName = "ApplyPatch"
					}
					state, created := getToolState(itemID, callID, cursorName)
					if created {
						if err := emitToolCall(state, adapteropenai.ToolCallFunction{Name: state.Name}); err != nil {
							return out, err
						}
					}
					out.SetFinishReason("tool_calls")
					input := rawString(item, "input")
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
				}
				continue
			}

			if eventNameLocal == "response.function_call_arguments.delta" {
				itemID := strings.TrimSpace(mapString(raw, "item_id"))
				delta := mapString(raw, "delta")
				state := toolCallsByItemID[itemID]
				if state != nil && delta != "" {
					state.ArgumentDeltaSeen = true
					out.SetFinishReason("tool_calls")
					if state.NativeName == "shell_command" {
						state.Arguments.WriteString(delta)
						continue
					}
					if err := emitToolCall(state, adapteropenai.ToolCallFunction{Arguments: delta}); err != nil {
						return out, err
					}
				}
				continue
			}

			if eventNameLocal == "response.custom_tool_call_input.delta" {
				itemID := strings.TrimSpace(mapString(raw, "item_id"))
				callID := strings.TrimSpace(mapString(raw, "call_id"))
				delta := rawString(raw, "delta")
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
				if delta, _ := raw["delta"].(string); delta != "" {
					kind := "text"
					var summaryIdx *int
					if strings.Contains(eventNameLocal, "summary") {
						kind = "summary"
						if v, ok := raw["summary_index"].(float64); ok {
							idx := int(v)
							summaryIdx = &idx
						}
					}
					if err := EmitRendered(renderer, adapterrender.Event{
						Kind:          adapterrender.EventReasoningDelta,
						Text:          delta,
						ReasoningKind: kind,
						SummaryIndex:  summaryIdx,
					}, emit, nil); err != nil {
						return RunResult{}, err
					}
				}
				continue
			}

			if eventNameLocal == "response.completed" {
				var c completedResponse
				b, _ := json.Marshal(raw)
				if err := json.Unmarshal(b, &c); err == nil {
					if responseID := strings.TrimSpace(c.Response.ID); responseID != "" {
						out.ResponseID = responseID
					}
					out.Usage = mapUsage(c)
					if out.FinishReason != "tool_calls" {
						out.SetFinishReason(finishreason.FromCodexResponse(c.Response.Status, c.Response.IncompleteDetails.Reason))
					}
				}
				out.ReasoningSignaled = reasoningTokens(raw) > 0
				if err := EmitRendered(renderer, adapterrender.Event{Kind: adapterrender.EventReasoningFinished}, emit, nil); err != nil {
					return out, err
				}
				renderer.Flush()
				state := renderer.State()
				out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
				out.ReasoningVisible = state.ReasoningVisible
				return out, nil
			}
			if eventNameLocal == "response.created" {
				if response, _ := raw["response"].(map[string]any); response != nil {
					out.ResponseID = strings.TrimSpace(mapString(response, "id"))
				}
				continue
			}
			if eventNameLocal == "response.failed" {
				_ = EmitRendered(renderer, adapterrender.Event{Kind: adapterrender.EventReasoningFinished}, emit, nil)
				renderer.Flush()
				msg := "codex response failed"
				if e, ok := raw["error"].(map[string]any); ok {
					if m, ok := e["message"].(string); ok && m != "" {
						msg = m
					}
				}
				return out, fmt.Errorf("%s", msg)
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := sc.Err(); err != nil {
		renderer.Flush()
		return out, err
	}
	renderer.Flush()
	state := renderer.State()
	out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
	out.ReasoningVisible = state.ReasoningVisible
	return out, nil
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

func reasoningTokens(raw map[string]any) int {
	response, _ := raw["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	details, _ := usage["output_tokens_details"].(map[string]any)
	switch v := details["reasoning_tokens"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func rawString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}
