package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type codexRPCMsg struct {
	ID     any             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *codexRPCClient) send(id int, method string, params any) error {
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.stdin, string(raw)+"\n")
	return err
}

func (c *codexRPCClient) next() (codexRPCMsg, error) {
	line, err := c.stdout.ReadString('\n')
	if err != nil {
		return codexRPCMsg{}, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return codexRPCMsg{}, io.EOF
	}
	var msg codexRPCMsg
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return codexRPCMsg{}, err
	}
	return msg, nil
}

func rpcIDEquals(v any, want int) bool {
	switch id := v.(type) {
	case float64:
		return int(id) == want
	case int:
		return id == want
	case string:
		return id == strconv.Itoa(want)
	default:
		return false
	}
}

func (s *Server) runCodexAppFallback(
	ctx context.Context,
	req ChatRequest,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	cctx, cancel := context.WithTimeout(ctx, s.codexAppFallbackTimeout())
	defer cancel()
	rpc, err := startCodexRPC(cctx, s.codexAppServerPath())
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	defer func() {
		_ = rpc.stdin.Close()
		_ = rpc.cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, rpc.stdout)
	}()

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
		return codexRunResult{FinishReason: "stop"}, err
	}
	if _, err := waitFor(1); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	rawInit, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})
	if _, err := io.WriteString(rpc.stdin, string(rawInit)+"\n"); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}

	system, prompt := BuildPrompt(req.Messages)
	threadID := ""
	if err := rpc.send(2, "thread/start", map[string]any{
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
		_ = rpc.send(9, "thread/archive", map[string]any{"threadId": threadID})
	}()

	if err := rpc.send(3, "turn/start", map[string]any{
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
		msg, err := rpc.next()
		if err != nil {
			return out, err
		}
		if msg.ID != nil && rpcIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, fmt.Errorf("codex turn/start: %s", msg.Error.Message)
			}
			continue
		}
		logAdapterProtocolEvent(ctx, reqID, "codex", msg.Method, slog.Int("params_bytes", len(msg.Params)))
		switch msg.Method {
		case "item/agentMessage/delta":
			var p struct{ Delta string `json:"delta"` }
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.Int("delta_len", len(p.Delta)))
			if p.Delta != "" {
				if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventAssistantTextDelta, Text: p.Delta}, emit, nil); err != nil {
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
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.Int("plan_steps", len(plan)), slog.Bool("has_explanation", strings.TrimSpace(p.Explanation) != ""))
			if ev, ok := codexPlanEvent(p.Explanation, plan); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/started", "item/completed":
			var p struct{ Item map[string]any `json:"item"` }
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_type", codexItemType(p.Item)), slog.String("item_status", codexItemStatus(p.Item)))
			if ev, ok := codexLifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
			var p struct {
				Delta  string `json:"delta"`
				ItemID string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("delta_len", len(p.Delta)))
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, p.Delta); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/mcpToolCall/progress":
			var p struct {
				Message string `json:"message"`
				ItemID  string `json:"itemId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("message_len", len(p.Message)))
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, p.Message); ok {
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/fileChange/patchUpdated":
			var p struct {
				ItemID  string `json:"itemId"`
				Changes []any  `json:"changes"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			logCodexToolingEvent(s.log, ctx, reqID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("change_count", len(p.Changes)))
			changeCount := len(p.Changes)
			if changeCount < 1 {
				changeCount = 1
			}
			if ev, ok := codexProgressEvent(msg.Method, p.ItemID, fmt.Sprintf("Patch updated for %d file(s)", changeCount)); ok {
				ev.ChangeCount = changeCount
				if err := emitCodexRendered(renderer, ev, emit, nil); err != nil {
					return out, err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct{ SummaryIndex int `json:"summaryIndex"` }
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			logCodexReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningSignaled}, emit, nil); err != nil {
				return out, err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p struct {
				Delta        string `json:"delta"`
				SummaryIndex int    `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			logCodexReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Int("delta_len", len(p.Delta)), slog.Bool("thinking_visible_before", renderer.State().ReasoningVisible))
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, nil); err != nil {
					return out, err
				}
			}
		case "turn/completed":
			if err := emitCodexRendered(renderer, tooltrans.Event{Kind: tooltrans.EventReasoningFinished}, emit, nil); err != nil {
				return out, err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			logCodexReasoningEvent(s.log, ctx, reqID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				logCodexToolingEvent(s.log, ctx, reqID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
			}
		}
	}
}
