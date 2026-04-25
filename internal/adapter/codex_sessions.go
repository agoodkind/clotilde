package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type codexManagedPromptPlan = adaptercodex.ManagedPromptPlan
type codexSessionSpec = adaptercodex.SessionSpec
type codexSessionAcquireResult = adaptercodex.SessionAcquireResult
type codexManagedTransport = adaptercodex.ManagedTransport
type codexManagedSession = adaptercodex.ManagedSession
type codexSessionManager = adaptercodex.SessionManager

type codexAppTransport struct {
	*adaptercodex.AppTransport
}

func normalizeCodexAssistantAnchor(text string) string {
	return adaptercodex.NormalizeAssistantAnchor(text, sanitizeForUpstreamCache)
}

func deriveCodexCacheCreationTokens(previousCachedInputTokens, currentCachedInputTokens int) int {
	return adaptercodex.DeriveCacheCreationTokens(previousCachedInputTokens, currentCachedInputTokens)
}

func buildCodexManagedPromptPlan(messages []ChatMessage) codexManagedPromptPlan {
	return adaptercodex.BuildManagedPromptPlan(messages, BuildPrompt, FlattenContent, sanitizeForUpstreamCache)
}

func newCodexSessionManager(log *slog.Logger, start func(spec codexSessionSpec) (codexManagedTransport, error)) *codexSessionManager {
	return adaptercodex.NewSessionManager(log, start)
}

func newCodexAppTransport(bin string, spec codexSessionSpec) (*codexAppTransport, error) {
	t, err := adaptercodex.NewAppTransport(bin, spec, adaptercodex.StartRPC)
	if err != nil {
		return nil, err
	}
	return &codexAppTransport{AppTransport: t}, nil
}

func (t *codexAppTransport) runTurn(ctx context.Context, requestID string, model string, effort any, summary any, prompt string, emit func(tooltrans.OpenAIStreamChunk) error) (codexRunResult, string, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = " "
	}
	if err := t.Send(3, "turn/start", map[string]any{
		"threadId":       t.ThreadID(),
		"approvalPolicy": "never",
		"model":          model,
		"effort":         effort,
		"summary":        summary,
		"input": []map[string]any{{
			"type": "text",
			"text": sanitizeForUpstreamCache(prompt),
		}},
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, "", err
	}

	out := codexRunResult{FinishReason: "stop"}
	var assistantText strings.Builder
	renderer := tooltrans.NewEventRenderer(requestID, model, "codex", slog.Default())
	for {
		select {
		case <-ctx.Done():
			return out, assistantText.String(), ctx.Err()
		default:
		}
		msg, err := t.Next()
		if err != nil {
			return out, assistantText.String(), err
		}
		if msg.ID != nil && adaptercodex.RPCIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, assistantText.String(), fmt.Errorf("codex turn/start: %s", msg.Error.Message)
			}
			continue
		}
		adaptercodex.LogProtocolEvent(ctx, requestID, "codex", msg.Method, slog.Int("params_bytes", len(msg.Params)))
		switch msg.Method {
		case "item/agentMessage/delta":
			var p struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(nil, ctx, requestID, msg.Method, slog.Int("delta_len", len(p.Delta)))
			if p.Delta != "" {
				if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventAssistantTextDelta, Text: p.Delta}, emit, &assistantText); err != nil {
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
			adaptercodex.LogToolingEvent(nil, ctx, requestID, msg.Method, slog.Int("plan_steps", len(plan)), slog.Bool("has_explanation", strings.TrimSpace(p.Explanation) != ""))
			if ev, ok := adaptercodex.PlanEvent(p.Explanation, plan); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/started", "item/completed":
			var p struct {
				Item map[string]any `json:"item"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_type", codexItemType(p.Item)), slog.String("item_status", codexItemStatus(p.Item)))
			if ev, ok := adaptercodex.LifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
			var p struct {
				Delta  string `json:"delta"`
				ItemID string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("delta_len", len(p.Delta)))
			if ev, ok := adaptercodex.ProgressEvent(msg.Method, p.ItemID, p.Delta); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/mcpToolCall/progress":
			var p struct {
				Message string `json:"message"`
				ItemID  string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("message_len", len(p.Message)))
			if ev, ok := adaptercodex.ProgressEvent(msg.Method, p.ItemID, p.Message); ok {
				if err := adaptercodex.EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/fileChange/patchUpdated":
			var p struct {
				ItemID  string `json:"itemId"`
				Changes []any  `json:"changes"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			adaptercodex.LogToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("change_count", len(p.Changes)))
			changeCount := len(p.Changes)
			if changeCount < 1 {
				changeCount = 1
			}
			if ev, ok := adaptercodex.ProgressEvent(msg.Method, p.ItemID, fmt.Sprintf("Patch updated for %d file(s)", changeCount)); ok {
				ev.ChangeCount = changeCount
				if err := adaptercodex.EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct {
				SummaryIndex int `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			adaptercodex.LogReasoningEvent(nil, ctx, requestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningSignaled}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p struct {
				Delta        string `json:"delta"`
				SummaryIndex int    `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			adaptercodex.LogReasoningEvent(nil, ctx, requestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Int("delta_len", len(p.Delta)), slog.Bool("thinking_visible_before", renderer.State().ReasoningVisible))
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, &assistantText); err != nil {
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
			derivedCacheCreate := deriveCodexCacheCreationTokens(t.CachedInputTokens(), currentCached)
			logAttrs := []slog.Attr{slog.Int("prompt_tokens", p.TokenUsage.Last.InputTokens), slog.Int("completion_tokens", p.TokenUsage.Last.OutputTokens), slog.Int("cached_input_tokens", currentCached), slog.Int("derived_cache_creation_tokens", derivedCacheCreate), slog.Int("reasoning_output_tokens", p.TokenUsage.Last.ReasoningOutputTokens), slog.Bool("native_cache_creation_metric_available", false)}
			adaptercodex.LogToolingEvent(nil, ctx, requestID, msg.Method, logAttrs...)
			out.Usage = Usage{PromptTokens: p.TokenUsage.Last.InputTokens, CompletionTokens: p.TokenUsage.Last.OutputTokens, TotalTokens: p.TokenUsage.Last.TotalTokens}
			if currentCached > 0 {
				out.Usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: currentCached}
			}
			out.DerivedCacheCreationTokens = derivedCacheCreate
			t.SetCachedInputTokens(currentCached)
			if p.TokenUsage.Last.ReasoningOutputTokens > 0 {
				out.ReasoningSignaled = true
			}
		case "turn/completed":
			if err := adaptercodex.EmitRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			adaptercodex.LogReasoningEvent(nil, ctx, requestID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, assistantText.String(), nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				adaptercodex.LogToolingEvent(nil, ctx, requestID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
			}
		}
	}
}

func codexManagedSummary(req ChatRequest) string {
	if r := effectiveCodexReasoning(req, ""); r != nil && r.Summary != "" {
		return r.Summary
	}
	return ""
}

func (s *Server) codexCursorContext(req ChatRequest) adaptercursor.Context {
	return adaptercursor.FromRequest(req)
}

func (s *Server) runCodexManaged(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, string, bool, error) {
	out, err := adaptercodex.RunManagedSession(
		codexManagedRuntime{server: s},
		ctx,
		s.codexSessions,
		req,
		s.codexCursorContext(req),
		model,
		effort,
		buildCodexManagedPromptPlan,
		reqID,
		emit,
	)
	if err != nil {
		return codexRunResult{}, "", out.Managed, err
	}
	res, _ := out.Result.(codexRunResult)
	return res, out.AssistantText, out.Managed, nil
}

type codexManagedRuntime struct {
	server *Server
}

func (rt codexManagedRuntime) Log() *slog.Logger { return rt.server.log }

func (rt codexManagedRuntime) NormalizeAssistantAnchor(text string) string {
	return normalizeCodexAssistantAnchor(text)
}

func (rt codexManagedRuntime) ManagedSummary(req ChatRequest) string { return codexManagedSummary(req) }

func (rt codexManagedRuntime) EffectiveAppEffort(req ChatRequest) any { return effectiveCodexAppEffort(req) }

func (rt codexManagedRuntime) EffectiveAppSummary(req ChatRequest) any { return effectiveCodexAppSummary(req) }

func (rt codexManagedRuntime) RunManagedTurn(
	ctx context.Context,
	session *adaptercodex.ManagedSession,
	spec adaptercodex.SessionSpec,
	reqID string,
	prompt string,
	effort any,
	summary any,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (any, string, error) {
	transport, _ := session.Transport.(*codexAppTransport)
	if transport == nil {
		return codexRunResult{}, "", adaptercodex.ManagedTransportTypeMismatch()
	}
	return transport.runTurn(ctx, reqID, spec.Model, effort, summary, prompt, emit)
}
