package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

type codexAppTransport struct {
	cancel                context.CancelFunc
	rpc                   *codexRPCClient
	threadID              string
	lastCachedInputTokens int
}

func newCodexAppTransport(bin string, spec codexSessionSpec) (*codexAppTransport, error) {
	sessCtx, cancel := context.WithCancel(context.Background())
	rpc, err := startCodexRPC(sessCtx, bin)
	if err != nil {
		cancel()
		return nil, err
	}
	t := &codexAppTransport{cancel: cancel, rpc: rpc}
	cleanup := func(err error) (*codexAppTransport, error) {
		_ = t.close()
		return nil, err
	}

	waitFor := func(id int) (codexRPCMsg, error) {
		for {
			msg, err := rpc.next()
			if err != nil {
				return codexRPCMsg{}, err
			}
			if msg.ID == nil || !rpcIDEquals(msg.ID, id) {
				continue
			}
			if msg.Error != nil {
				return codexRPCMsg{}, fmt.Errorf("codex rpc %s", msg.Error.Message)
			}
			return msg, nil
		}
	}

	if err := rpc.send(1, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "clyde-adapter",
			"title":   "Clyde Adapter",
			"version": "0.1.0",
		},
	}); err != nil {
		return cleanup(err)
	}
	if _, err := waitFor(1); err != nil {
		return cleanup(err)
	}
	rawInit, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	if _, err := io.WriteString(rpc.stdin, string(rawInit)+"\n"); err != nil {
		return cleanup(err)
	}
	if err := rpc.send(2, "thread/start", map[string]any{
		"model":                  spec.Model,
		"cwd":                    ".",
		"approvalPolicy":         "never",
		"ephemeral":              true,
		"serviceName":            "clyde-codex-session",
		"baseInstructions":       spec.System,
		"experimentalRawEvents":  false,
		"persistExtendedHistory": false,
	}); err != nil {
		return cleanup(err)
	}
	startResp, err := waitFor(2)
	if err != nil {
		return cleanup(err)
	}
	var threadResp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(startResp.Result, &threadResp); err != nil {
		return cleanup(err)
	}
	t.threadID = threadResp.Thread.ID
	return t, nil
}

func (t *codexAppTransport) close() error {
	if t == nil {
		return nil
	}
	if t.rpc != nil && t.threadID != "" {
		_ = t.rpc.send(9, "thread/archive", map[string]any{"threadId": t.threadID})
	}
	if t.rpc != nil && t.rpc.stdin != nil {
		_ = t.rpc.stdin.Close()
	}
	if t.cancel != nil {
		t.cancel()
	}
	if t.rpc != nil && t.rpc.cmd != nil && t.rpc.cmd.Process != nil {
		_ = t.rpc.cmd.Process.Kill()
	}
	if t.rpc != nil && t.rpc.stdout != nil {
		_, _ = io.Copy(io.Discard, t.rpc.stdout)
	}
	return nil
}

func (t *codexAppTransport) Close() error { return t.close() }

func (t *codexAppTransport) runTurn(ctx context.Context, requestID string, model string, effort any, summary any, prompt string, emit func(tooltrans.OpenAIStreamChunk) error) (codexRunResult, string, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = " "
	}
	if err := t.rpc.send(3, "turn/start", map[string]any{
		"threadId":       t.threadID,
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
		msg, err := t.rpc.next()
		if err != nil {
			return out, assistantText.String(), err
		}
		if msg.ID != nil && rpcIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, assistantText.String(), fmt.Errorf("codex turn/start: %s", msg.Error.Message)
			}
			continue
		}
		logAdapterProtocolEvent(ctx, requestID, "codex", msg.Method, slog.Int("params_bytes", len(msg.Params)))
		switch msg.Method {
		case "item/agentMessage/delta":
			var p struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(nil, ctx, requestID, msg.Method, slog.Int("delta_len", len(p.Delta)))
			if p.Delta != "" {
				if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventAssistantTextDelta, Text: p.Delta}, emit, &assistantText); err != nil {
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
			logCodexToolingEvent(nil, ctx, requestID, msg.Method, slog.Int("plan_steps", len(plan)), slog.Bool("has_explanation", strings.TrimSpace(p.Explanation) != ""))
			if ev, ok := codexPlanEvent(p.Explanation, plan); ok {
				if err := emitCodexRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/started", "item/completed":
			var p struct {
				Item map[string]any `json:"item"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_type", codexItemType(p.Item)), slog.String("item_status", codexItemStatus(p.Item)))
			if ev, ok := codexLifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := emitCodexRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
			var p struct {
				Delta  string `json:"delta"`
				ItemID string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("delta_len", len(p.Delta)))
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, p.Delta); ok {
				if err := emitCodexRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/mcpToolCall/progress":
			var p struct {
				Message string `json:"message"`
				ItemID  string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("message_len", len(p.Message)))
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, p.Message); ok {
				if err := emitCodexRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/fileChange/patchUpdated":
			var p struct {
				ItemID  string `json:"itemId"`
				Changes []any  `json:"changes"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(nil, ctx, requestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("change_count", len(p.Changes)))
			changeCount := len(p.Changes)
			if changeCount < 1 {
				changeCount = 1
			}
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, fmt.Sprintf("Patch updated for %d file(s)", changeCount)); ok {
				ev.ChangeCount = changeCount
				if err := emitCodexRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct {
				SummaryIndex int `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			logCodexReasoningEvent(nil, ctx, requestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningSignaled}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p struct {
				Delta        string `json:"delta"`
				SummaryIndex int    `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			logCodexReasoningEvent(nil, ctx, requestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Int("delta_len", len(p.Delta)), slog.Bool("thinking_visible_before", renderer.State().ReasoningVisible))
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, &assistantText); err != nil {
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
			derivedCacheCreate := deriveCodexCacheCreationTokens(t.lastCachedInputTokens, currentCached)
			logAttrs := []slog.Attr{slog.Int("prompt_tokens", p.TokenUsage.Last.InputTokens), slog.Int("completion_tokens", p.TokenUsage.Last.OutputTokens), slog.Int("cached_input_tokens", currentCached), slog.Int("derived_cache_creation_tokens", derivedCacheCreate), slog.Int("reasoning_output_tokens", p.TokenUsage.Last.ReasoningOutputTokens), slog.Bool("native_cache_creation_metric_available", false)}
			logCodexToolingEvent(nil, ctx, requestID, msg.Method, logAttrs...)
			out.Usage = Usage{PromptTokens: p.TokenUsage.Last.InputTokens, CompletionTokens: p.TokenUsage.Last.OutputTokens, TotalTokens: p.TokenUsage.Last.TotalTokens}
			if currentCached > 0 {
				out.Usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: currentCached}
			}
			out.DerivedCacheCreationTokens = derivedCacheCreate
			t.lastCachedInputTokens = currentCached
			if p.TokenUsage.Last.ReasoningOutputTokens > 0 {
				out.ReasoningSignaled = true
			}
		case "turn/completed":
			if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			logCodexReasoningEvent(nil, ctx, requestID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, assistantText.String(), nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				logCodexToolingEvent(nil, ctx, requestID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
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
	if s.codexSessions == nil {
		return codexRunResult{}, "", false, nil
	}
	cursor := s.codexCursorContext(req)
	sessionKey := cursor.StrongConversationKey()
	if sessionKey == "" {
		s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.codex.session.not_admitted",
			slog.String("request_id", reqID),
			slog.String("reason", "missing_cursor_conversation_id"),
			slog.String("cursor_request_id", cursor.RequestID),
		)
		return codexRunResult{}, "", false, nil
	}

	plan := buildCodexManagedPromptPlan(req.Messages)
	spec := codexSessionSpec{
		Key:     sessionKey,
		Model:   strings.TrimSpace(model.ClaudeModel),
		Effort:  strings.ToLower(strings.TrimSpace(effort)),
		Summary: strings.ToLower(strings.TrimSpace(codexManagedSummary(req))),
		System:  plan.System,
	}
	if spec.Model == "" {
		spec.Model = strings.TrimSpace(model.Alias)
	}

	acquired, err := s.codexSessions.Acquire(ctx, spec)
	if err != nil {
		return codexRunResult{}, "", false, err
	}
	session := acquired.Session
	defer s.codexSessions.Release(session)

	prompt := plan.IncrementalPrompt
	promptMode := "incremental"
	resetReason := acquired.ResetReason
	if acquired.Created {
		prompt = plan.FullPrompt
		promptMode = "full"
	}

	if !acquired.Created {
		switch {
		case session.LastAssistant == "" && plan.AssistantAnchor != "":
			resetReason = "assistant_anchor_missing"
		case session.LastAssistant != "" && plan.AssistantAnchor == "":
			resetReason = "assistant_anchor_dropped"
		case session.LastAssistant != "" && plan.AssistantAnchor != "" && session.LastAssistant != plan.AssistantAnchor:
			resetReason = "assistant_anchor_mismatch"
		}
		if resetReason != "" {
			s.codexSessions.Drop(session, resetReason)
			acquired, err = s.codexSessions.Acquire(ctx, spec)
			if err != nil {
				return codexRunResult{}, "", false, err
			}
			session = acquired.Session
			defer s.codexSessions.Release(session)
			prompt = plan.FullPrompt
			promptMode = "full"
		}
	}

	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.codex.session.admitted",
		slog.String("request_id", reqID),
		slog.String("session_key", sessionKey),
		slog.String("cursor_conversation_id", cursor.ConversationID),
		slog.String("cursor_request_id", cursor.RequestID),
		slog.Bool("created", acquired.Created),
		slog.String("prompt_mode", promptMode),
	)

	session.RunMu.Lock()
	defer session.RunMu.Unlock()
	transport, _ := session.Transport.(*codexAppTransport)
	if transport == nil {
		return codexRunResult{}, "", true, fmt.Errorf("codex session transport type mismatch")
	}
	res, assistantText, err := transport.runTurn(ctx, reqID, spec.Model, effectiveCodexAppEffort(req), effectiveCodexAppSummary(req), prompt, emit)
	if err != nil {
		s.codexSessions.Drop(session, "transport_error")
		return codexRunResult{}, "", true, err
	}
	session.LastAssistant = normalizeCodexAssistantAnchor(assistantText)
	return res, assistantText, true, nil
}
