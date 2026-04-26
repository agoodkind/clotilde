package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
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

type AppTurnTransport interface {
	Send(id int, method string, params any) error
	Next() (RPCMessage, error)
	ThreadID() string
	CachedInputTokens() int
	SetCachedInputTokens(int)
}

type AppTurnConfig struct {
	RequestID      string
	Model          string
	Effort         any
	Summary        any
	Prompt         string
	SanitizePrompt func(string) string
	Logger         *slog.Logger
}

func RunManagedTurn(
	ctx context.Context,
	transport AppTurnTransport,
	cfg AppTurnConfig,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (RunResult, string, error) {
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = " "
	}
	sanitize := cfg.SanitizePrompt
	if sanitize == nil {
		sanitize = SanitizeForUpstreamCache
	}
	if err := transport.Send(3, "turn/start", map[string]any{
		"threadId":       transport.ThreadID(),
		"approvalPolicy": "never",
		"model":          cfg.Model,
		"effort":         cfg.Effort,
		"summary":        cfg.Summary,
		"input": []map[string]any{{
			"type": "text",
			"text": sanitize(prompt),
		}},
	}); err != nil {
		return NewRunResult("stop"), "", err
	}

	out := NewRunResult("stop")
	var assistantText strings.Builder
	renderer := tooltrans.NewEventRenderer(cfg.RequestID, cfg.Model, "codex", cfg.Logger)
	for {
		select {
		case <-ctx.Done():
			return out, assistantText.String(), ctx.Err()
		default:
		}
		msg, err := transport.Next()
		if err != nil {
			return out, assistantText.String(), err
		}
		if msg.ID != nil && RPCIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, assistantText.String(), fmt.Errorf("codex turn/start: %s", msg.Error.Message)
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
				if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventAssistantTextDelta, Text: p.Delta}, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
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
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/started", "item/completed":
			var p struct {
				Item map[string]any `json:"item"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_type", itemType(p.Item)), slog.String("item_status", itemStatus(p.Item)))
			if ev, ok := LifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
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
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
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
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
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
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct {
				SummaryIndex int `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningSignaled}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
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
				if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "thread/tokenUsage/updated":
			var p struct {
				TokenUsage struct {
					Last struct {
						TotalTokens           int `json:"totalTokens"`
						InputTokens           int `json:"inputTokens"`
						CachedInputTokens     int `json:"cachedInputTokens"`
						OutputTokens          int `json:"outputTokens"`
						ReasoningOutputTokens int `json:"reasoningOutputTokens"`
					} `json:"last"`
				} `json:"tokenUsage"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			currentCached := p.TokenUsage.Last.CachedInputTokens
			derivedCacheCreate := DeriveCacheCreationTokens(transport.CachedInputTokens(), currentCached)
			logAttrs := []slog.Attr{slog.Int("prompt_tokens", p.TokenUsage.Last.InputTokens), slog.Int("completion_tokens", p.TokenUsage.Last.OutputTokens), slog.Int("cached_input_tokens", currentCached), slog.Int("derived_cache_creation_tokens", derivedCacheCreate), slog.Int("reasoning_output_tokens", p.TokenUsage.Last.ReasoningOutputTokens), slog.Bool("native_cache_creation_metric_available", false)}
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, logAttrs...)
			out.Usage.PromptTokens = p.TokenUsage.Last.InputTokens
			out.Usage.CompletionTokens = p.TokenUsage.Last.OutputTokens
			out.Usage.TotalTokens = p.TokenUsage.Last.TotalTokens
			if currentCached > 0 {
				out.Usage.PromptTokensDetails = &adapteropenai.PromptTokensDetails{CachedTokens: currentCached}
			}
			out.DerivedCacheCreationTokens = derivedCacheCreate
			transport.SetCachedInputTokens(currentCached)
			if p.TokenUsage.Last.ReasoningOutputTokens > 0 {
				out.ReasoningSignaled = true
			}
		case "turn/completed":
			if err := EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, assistantText.String(), nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
			}
		}
	}
}

type rpcAppTurnTransport struct {
	rpc      RPCClient
	threadID string
	cached   int
}

func (t *rpcAppTurnTransport) Send(id int, method string, params any) error {
	return t.rpc.Send(id, method, params)
}

func (t *rpcAppTurnTransport) Next() (RPCMessage, error) { return t.rpc.Next() }

func (t *rpcAppTurnTransport) ThreadID() string { return t.threadID }

func (t *rpcAppTurnTransport) CachedInputTokens() int { return t.cached }

func (t *rpcAppTurnTransport) SetCachedInputTokens(v int) { t.cached = v }

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

	out, _, err := RunManagedTurn(ctx, &rpcAppTurnTransport{rpc: rpc, threadID: threadID}, AppTurnConfig{
		RequestID:      cfg.RequestID,
		Model:          cfg.Model,
		Effort:         cfg.Effort,
		Summary:        cfg.Summary,
		Prompt:         cfg.Prompt,
		SanitizePrompt: cfg.SanitizePrompt,
		Logger:         cfg.Logger,
	}, emit)
	return out, err
}
