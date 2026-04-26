package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type AppFallbackConfig struct {
	Binary         string
	RequestID      string
	Model          string
	Effort         any
	Summary        any
	SystemPrompt   string
	Prompt         string
	SanitizePrompt func(string) string
	StartRPC       RPCStarter
	Logger         *slog.Logger
}

func RunAppFallback(ctx context.Context, cfg AppFallbackConfig, emit func(tooltrans.OpenAIStreamChunk) error) (RunResult, error) {
	rpc, err := cfg.StartRPC(ctx, cfg.Binary)
	if err != nil {
		return NewRunResult("stop"), err
	}
	defer func() { _ = rpc.Close() }()

	waitFor := func(id int) (RPCMessage, error) {
		for {
			msg, err := rpc.Next()
			if err != nil {
				return RPCMessage{}, err
			}
			if msg.ID == nil || !RPCIDEquals(msg.ID, id) {
				continue
			}
			if msg.Error != nil {
				return RPCMessage{}, fmt.Errorf("codex rpc %s", strings.TrimSpace(msg.Error.Message))
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
		return NewRunResult("stop"), err
	}
	if _, err := waitFor(1); err != nil {
		return NewRunResult("stop"), err
	}
	if err := rpc.Notify("initialized", map[string]any{}); err != nil {
		return NewRunResult("stop"), err
	}

	threadID := ""
	if err := rpc.Send(2, "thread/start", map[string]any{
		"cwd":              ".",
		"approvalPolicy":   "never",
		"ephemeral":        true,
		"model":            strings.TrimSpace(cfg.Model),
		"reasoningEffort":  cfg.Effort,
		"reasoningSummary": cfg.Summary,
		"systemPrompt":     strings.TrimSpace(cfg.SystemPrompt),
	}); err != nil {
		return NewRunResult("stop"), err
	}
	threadMsg, err := waitFor(2)
	if err != nil {
		return NewRunResult("stop"), err
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

	prompt := cfg.Prompt
	if cfg.SanitizePrompt != nil {
		prompt = cfg.SanitizePrompt(prompt)
	}
	if err := rpc.Send(3, "turn/start", map[string]any{
		"threadId":       threadID,
		"approvalPolicy": "never",
		"effort":         cfg.Effort,
		"summary":        cfg.Summary,
		"input": []map[string]any{{
			"type": "text",
			"text": prompt,
		}},
	}); err != nil {
		return NewRunResult("stop"), err
	}

	out := NewRunResult("stop")
	renderer := tooltrans.NewEventRenderer(cfg.RequestID, cfg.Model, "codex", cfg.Logger)
	for {
		msg, err := rpc.Next()
		if err != nil {
			return out, err
		}
		if msg.ID != nil && RPCIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, fmt.Errorf("codex turn/start: %s", strings.TrimSpace(msg.Error.Message))
			}
			continue
		}
		LogProtocolEvent(ctx, cfg.RequestID, "codex", msg.Method, slog.Int("params_bytes", len(msg.Params)))
		switch msg.Method {
		case "item/agentMessage/delta":
			var p struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("delta_len", len(p.Delta)))
			if p.Delta != "" {
				if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventAssistantTextDelta, Text: p.Delta}, emit, nil); err != nil {
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
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("plan_steps", len(plan)), slog.Bool("has_explanation", strings.TrimSpace(p.Explanation) != ""))
			if ev, ok := PlanEvent(p.Explanation, plan); ok {
				if err := EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/started", "item/completed":
			var p struct {
				Item map[string]any `json:"item"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_type", itemType(p.Item)), slog.String("item_status", itemStatus(p.Item)))
			if ev, ok := LifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
			var p struct {
				Delta  string `json:"delta"`
				ItemID string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("delta_len", len(p.Delta)))
			if ev, ok := ProgressEvent(msg.Method, p.ItemID, p.Delta); ok {
				if err := EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/mcpToolCall/progress":
			var p struct {
				Message string `json:"message"`
				ItemID  string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("message_len", len(p.Message)))
			if ev, ok := ProgressEvent(msg.Method, p.ItemID, p.Message); ok {
				if err := EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/fileChange/patchUpdated":
			var p struct {
				ItemID  string `json:"itemId"`
				Changes []any  `json:"changes"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("change_count", len(p.Changes)))
			changeCount := len(p.Changes)
			if changeCount < 1 {
				changeCount = 1
			}
			if ev, ok := ProgressEvent(msg.Method, p.ItemID, fmt.Sprintf("Patch updated for %d file(s)", changeCount)); ok {
				ev.ChangeCount = changeCount
				if err := EmitRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct {
				SummaryIndex int `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningSignaled}, emit, nil); err != nil {
				return out, err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p struct {
				Delta        string `json:"delta"`
				SummaryIndex int    `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Int("delta_len", len(p.Delta)), slog.Bool("thinking_visible_before", renderer.State().ReasoningVisible))
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, nil); err != nil {
					return out, err
				}
			}
		case "turn/completed":
			if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, nil); err != nil {
				return out, err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
			}
		}
	}
}
