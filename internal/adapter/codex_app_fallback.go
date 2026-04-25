package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

func (s *Server) runCodexAppFallback(
	ctx context.Context,
	req ChatRequest,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	cctx, cancel := context.WithTimeout(ctx, s.codexAppFallbackTimeout())
	defer cancel()
	rpc, err := adaptercodex.StartRPC(cctx, s.codexAppServerPath())
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	defer func() { _ = rpc.Close() }()

	waitFor := func(id int) (adaptercodex.RPCMessage, error) {
		for {
			msg, err := rpc.Next()
			if err != nil {
				return adaptercodex.RPCMessage{}, err
			}
			if msg.ID == nil || !adaptercodex.RPCIDEquals(msg.ID, id) {
				continue
			}
			if msg.Error != nil {
				return adaptercodex.RPCMessage{}, fmt.Errorf("codex rpc %s", msg.Error.Message)
			}
			return msg, nil
		}
	}

	if err := rpc.Send(1, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "clyde-adapter",
			"title":   "Clyde Adapter",
			"version": "0.1.0",
		},
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	if _, err := waitFor(1); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	if err := rpc.Notify("initialized", map[string]any{}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}

	system, prompt := BuildPrompt(req.Messages)
	threadID := ""
	if err := rpc.Send(2, "thread/start", map[string]any{
		"cwd":              ".",
		"approvalPolicy":   "never",
		"ephemeral":        true,
		"model":            strings.TrimSpace(req.Model),
		"reasoningEffort":  effectiveCodexAppEffort(req),
		"reasoningSummary": effectiveCodexAppSummary(req),
		"systemPrompt":     strings.TrimSpace(system),
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	threadMsg, err := waitFor(2)
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	if len(threadMsg.Result) > 0 {
		var r struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(threadMsg.Result, &r)
		threadID = strings.TrimSpace(r.ThreadID)
	}
	defer func() {
		if threadID == "" {
			return
		}
		_ = rpc.Send(9, "thread/archive", map[string]any{"threadId": threadID})
	}()

	if err := rpc.Send(3, "turn/start", map[string]any{
		"threadId":       threadID,
		"approvalPolicy": "never",
		"effort":         effectiveCodexAppEffort(req),
		"summary":        effectiveCodexAppSummary(req),
		"input": []map[string]any{{
			"type": "text",
			"text": sanitizeForUpstreamCache(prompt),
		}},
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}

	out := codexRunResult{FinishReason: "stop"}
	renderer := tooltrans.NewEventRenderer(reqID, req.Model, "codex", s.log)
	for {
		msg, err := rpc.Next()
		if err != nil {
			return out, err
		}
		if msg.ID != nil && adaptercodex.RPCIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, fmt.Errorf("codex turn/start: %s", msg.Error.Message)
			}
			continue
		}
		adaptercodex.LogProtocolEvent(ctx, reqID, "codex", msg.Method, slog.Int("params_bytes", len(msg.Params)))
		switch msg.Method {
		case "item/agentMessage/delta":
			var p struct{ Delta string `json:"delta"` }
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(s.log, ctx, reqID, msg.Method, slog.Int("delta_len", len(p.Delta)))
			if p.Delta != "" {
				if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventAssistantTextDelta, Text: p.Delta}, emit, nil); err != nil {
					return out, err
				}
			}
		case "turn/plan/updated":
			var p struct {
				Explanation string `json:"explanation"`
				Plan        []struct {
					Step   string `json:"step"`
					Status string `json:"status"`
				} `json:"plan"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			plan := make([]map[string]string, 0, len(p.Plan))
			for _, step := range p.Plan {
				plan = append(plan, map[string]string{"step": step.Step, "status": step.Status})
			}
			adaptercodex.LogToolingEvent(s.log, ctx, reqID, msg.Method, slog.Int("plan_steps", len(plan)), slog.Bool("has_explanation", strings.TrimSpace(p.Explanation) != ""))
			if ev, ok := adaptercodex.PlanEvent(p.Explanation, plan); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/started", "item/completed":
			var p struct{ Item map[string]any `json:"item"` }
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_type", codexItemType(p.Item)), slog.String("item_status", codexItemStatus(p.Item)))
			if ev, ok := adaptercodex.LifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
			var p struct {
				Delta  string `json:"delta"`
				ItemID string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("delta_len", len(p.Delta)))
			if ev, ok := adaptercodex.ProgressEvent(msg.Method, p.ItemID, p.Delta); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/mcpToolCall/progress":
			var p struct {
				Message string `json:"message"`
				ItemID  string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("message_len", len(p.Message)))
			if ev, ok := adaptercodex.ProgressEvent(msg.Method, p.ItemID, p.Message); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/fileChange/patchUpdated":
			var p struct {
				ItemID  string `json:"itemId"`
				Changes []any  `json:"changes"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("change_count", len(p.Changes)))
			changeCount := len(p.Changes)
			if changeCount < 1 {
				changeCount = 1
			}
			if ev, ok := adaptercodex.ProgressEvent(msg.Method, p.ItemID, fmt.Sprintf("Patch updated for %d file(s)", changeCount)); ok {
				ev.ChangeCount = changeCount
				if err := adaptercodex.EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct{ SummaryIndex int `json:"summaryIndex"` }
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			adaptercodex.LogReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningSignaled}, emit, nil); err != nil {
				return out, err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p struct {
				Delta        string `json:"delta"`
				SummaryIndex int    `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			adaptercodex.LogReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Int("delta_len", len(p.Delta)), slog.Bool("thinking_visible_before", renderer.State().ReasoningVisible))
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, nil); err != nil {
					return out, err
				}
			}
		case "turn/completed":
			if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, nil); err != nil {
				return out, err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			adaptercodex.LogReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				adaptercodex.LogToolingEvent(s.log, ctx, reqID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
			}
		}
	}
}
