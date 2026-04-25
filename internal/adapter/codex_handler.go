package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/chatemit"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type codexInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexInputItem struct {
	Role    string              `json:"role"`
	Content []codexInputContent `json:"content"`
}

type codexRequest struct {
	Model        string           `json:"model"`
	Instructions string           `json:"instructions"`
	Store        bool             `json:"store"`
	Stream       bool             `json:"stream"`
	Include      []string         `json:"include,omitempty"`
	PromptCache  string           `json:"prompt_cache_key,omitempty"`
	Reasoning    *codexReasoning  `json:"reasoning,omitempty"`
	Input        []codexInputItem `json:"input"`
}

type codexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type codexCompleted struct {
	Response struct {
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

type codexRunResult struct {
	Usage             Usage
	FinishReason      string
	ReasoningSignaled bool
	ReasoningVisible  bool
}

type codexReasoningStreamState struct {
	LastKind       string
	LastSummaryIdx int
	HaveSummaryIdx bool
	PendingBreak   bool
}

func sanitizeForUpstreamCache(text string) string {
	text = tooltrans.StripNoticeSentinel(text)
	text = tooltrans.StripThinkingSentinel(text)
	return text
}

func mapCodexUsage(c codexCompleted) Usage {
	u := Usage{
		PromptTokens:     c.Response.Usage.InputTokens,
		CompletionTokens: c.Response.Usage.OutputTokens,
		TotalTokens:      c.Response.Usage.TotalTokens,
	}
	if ct := c.Response.Usage.InputTokensDetails.CachedTokens; ct > 0 {
		u.PromptTokensDetails = &PromptTokensDetails{CachedTokens: ct}
	}
	return u
}

func codexReasoningTokens(raw map[string]any) int {
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

func codexReasoningPlaceholder() string {
	return tooltrans.ThinkingInlineOpen() + tooltrans.ThinkingInlineClose()
}

func (s *codexReasoningStreamState) nextChunk(open bool, kind string, summaryIdx *int, delta string) string {
	if delta == "" {
		return ""
	}
	if open {
		s.LastKind = kind
		if summaryIdx != nil {
			s.LastSummaryIdx = *summaryIdx
			s.HaveSummaryIdx = true
		}
		return tooltrans.FormatThinkingInlineDelta(true, delta)
	}
	prefix := ""
	if s.PendingBreak {
		prefix = "\n\n"
		s.PendingBreak = false
	} else if s.LastKind != "" && s.LastKind != kind {
		prefix = "\n\n"
	}
	trimmed := strings.TrimSpace(delta)
	if kind == "summary" && !open && strings.HasPrefix(trimmed, "**") {
		prefix = "\n\n"
	}
	if kind == "summary" && summaryIdx != nil && s.HaveSummaryIdx && s.LastSummaryIdx != *summaryIdx {
		prefix = "\n\n"
	}
	s.LastKind = kind
	if summaryIdx != nil {
		s.LastSummaryIdx = *summaryIdx
		s.HaveSummaryIdx = true
	}
	return tooltrans.FormatThinkingInlineDelta(false, prefix+delta)
}

func buildCodexRequest(req ChatRequest, model ResolvedModel, effort string) codexRequest {
	var instr []string
	input := make([]codexInputItem, 0, len(req.Messages))
	modelName := strings.TrimSpace(model.ClaudeModel)
	if modelName == "" {
		modelName = model.Alias
	}
	for _, m := range req.Messages {
		text := sanitizeForUpstreamCache(FlattenContent(m.Content))
		text = strings.TrimSpace(text)
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			if text != "" {
				instr = append(instr, text)
			}
			continue
		case "assistant":
			if text != "" {
				input = append(input, codexInputItem{
					Role: "assistant",
					Content: []codexInputContent{{
						Type: "input_text",
						Text: text,
					}},
				})
			}
		case "tool", "function":
			if text != "" {
				input = append(input, codexInputItem{
					Role: "user",
					Content: []codexInputContent{{
						Type: "input_text",
						Text: "tool: " + text,
					}},
				})
			}
		default:
			if text != "" {
				input = append(input, codexInputItem{
					Role: "user",
					Content: []codexInputContent{{
						Type: "input_text",
						Text: text,
					}},
				})
			}
		}
	}
	instructions := strings.TrimSpace(strings.Join(instr, "\n\n"))
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}
	if len(input) == 0 {
		input = append(input, codexInputItem{
			Role: "user",
			Content: []codexInputContent{{
				Type: "input_text",
				Text: " ",
			}},
		})
	}
	reasoning := effectiveCodexReasoning(req, effort)
	include := codexRequestInclude(req.Include, reasoning != nil)
	promptCacheKey := requestContextTrackerKey(req, model.Alias)
	return codexRequest{
		Model:        modelName,
		Instructions: instructions,
		Store:        false,
		Stream:       true,
		Include:      include,
		PromptCache:  promptCacheKey,
		Reasoning:    reasoning,
		Input:        input,
	}
}

func codexRequestInclude(requested []string, reasoningEnabled bool) []string {
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

func effectiveCodexReasoning(req ChatRequest, effort string) *codexReasoning {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		effort = strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	}
	if effort == "" && req.Reasoning != nil {
		effort = strings.ToLower(strings.TrimSpace(req.Reasoning.Effort))
	}
	var out codexReasoning
	switch effort {
	case EffortLow, EffortMedium, EffortHigh:
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

func effectiveCodexAppEffort(req ChatRequest) any {
	if r := effectiveCodexReasoning(req, ""); r != nil && r.Effort != "" {
		return r.Effort
	}
	return nil
}

func effectiveCodexAppSummary(req ChatRequest) any {
	if r := effectiveCodexReasoning(req, ""); r != nil && r.Summary != "" {
		return r.Summary
	}
	return nil
}

func parseCodexSSE(
	body io.Reader,
	onDelta func(text string) error,
	onThinking func(text string) error,
) (codexRunResult, error) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 1024*128), 1024*1024*8)

	var eventName string
	var dataLines []string
	out := codexRunResult{FinishReason: "stop"}
	thinkingOpen := false
	thinkingVisible := false
	var reasoningState codexReasoningStreamState
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
				if thinkingOpen {
					if err := onDelta(tooltrans.ThinkingInlineClose()); err != nil {
						return out, err
					}
					thinkingOpen = false
				}
				if delta, _ := raw["delta"].(string); delta != "" {
					if err := onDelta(delta); err != nil {
						return out, err
					}
				}
				continue
			}

			if strings.Contains(eventNameLocal, "reasoning") && strings.HasSuffix(eventNameLocal, ".delta") {
				if delta, _ := raw["delta"].(string); delta != "" {
					kind := "text"
					if strings.Contains(eventNameLocal, "summary") {
						kind = "summary"
					}
					out := reasoningState.nextChunk(!thinkingOpen, kind, nil, delta)
					thinkingOpen = true
					thinkingVisible = true
					if err := onThinking(out); err != nil {
						return codexRunResult{}, err
					}
				}
				continue
			}

			if eventNameLocal == "response.completed" {
				var c codexCompleted
				b, _ := json.Marshal(raw)
				if err := json.Unmarshal(b, &c); err == nil {
					out.Usage = mapCodexUsage(c)
				}
				out.ReasoningSignaled = codexReasoningTokens(raw) > 0
				if thinkingOpen {
					if err := onThinking(tooltrans.ThinkingInlineClose()); err != nil {
						return out, err
					}
					thinkingOpen = false
				}
				out.ReasoningVisible = thinkingVisible
				return out, nil
			}
			if eventNameLocal == "response.failed" {
				if thinkingOpen {
					if err := onThinking(tooltrans.ThinkingInlineClose()); err != nil {
						return out, err
					}
				}
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
		return out, err
	}
	out.ReasoningVisible = thinkingVisible
	return out, nil
}

func (s *Server) runCodexDirect(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	onDelta func(text string) error,
) (codexRunResult, error) {
	token, err := s.readCodexAccessToken()
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	payload := buildCodexRequest(req, model, effort)
	raw, err := json.Marshal(payload)
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.codexBaseURL(), bytes.NewReader(raw))
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if conversationID := payload.PromptCache; conversationID != "" {
		httpReq.Header.Set("x-client-request-id", conversationID)
		httpReq.Header.Set("x-codex-window-id", conversationID+":0")
	}

	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return codexRunResult{FinishReason: "stop"}, fmt.Errorf("codex backend %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return parseCodexSSE(resp.Body, onDelta, onDelta)
}

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
	onDelta func(text string) error,
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
		"serviceName":      "clyde-fallback",
		"baseInstructions": system,
	}); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	startResp, err := waitFor(2)
	if err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	var threadResp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(startResp.Result, &threadResp); err != nil {
		return codexRunResult{FinishReason: "stop"}, err
	}
	threadID = threadResp.Thread.ID
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
	thinkingVisible := false
	var reasoningState codexReasoningStreamState
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
		switch msg.Method {
		case "item/agentMessage/delta":
			var p struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Delta != "" {
				if err := onDelta(p.Delta); err != nil {
					return out, err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p struct {
				SummaryIndex int `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			if thinkingVisible {
				if !reasoningState.HaveSummaryIdx || reasoningState.LastSummaryIdx != p.SummaryIndex {
					reasoningState.PendingBreak = true
				}
			}
			reasoningState.LastSummaryIdx = p.SummaryIndex
			reasoningState.HaveSummaryIdx = true
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p struct {
				Delta        string `json:"delta"`
				SummaryIndex int    `json:"summaryIndex"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				chunk := reasoningState.nextChunk(!thinkingVisible, kind, summaryIdx, p.Delta)
				thinkingVisible = true
				if err := onDelta(chunk); err != nil {
					return out, err
				}
			}
		case "turn/completed":
			if out.ReasoningSignaled && !thinkingVisible {
				if err := onDelta(codexReasoningPlaceholder()); err != nil {
					return out, err
				}
			}
			out.ReasoningVisible = thinkingVisible
			return out, nil
		}
	}
}

func (s *Server) collectCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	var text strings.Builder
	path := "direct"
	s.emitRequestStarted(r.Context(), model, path, reqID, model.Alias, false)
	res, _, managed, err := s.runCodexManaged(r.Context(), req, model, effort, reqID, func(delta string) error {
		text.WriteString(delta)
		return nil
	})
	if managed {
		path = "app"
	}
	if !managed && err == nil {
		res, err = s.runCodexDirect(r.Context(), req, model, effort, reqID, func(delta string) error {
			text.WriteString(delta)
			return nil
		})
	}
	if err != nil && !managed && s.cfg.Codex.AppFallback {
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   providerName(model, "direct"),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.Alias,
			Stream:     false,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.codex.fallback.escalating",
			slog.String("request_id", reqID),
			slog.Any("err", err),
		)
		text.Reset()
		path = "app"
		s.emitRequestStarted(r.Context(), model, path, reqID, model.Alias, false)
		res, err = s.runCodexAppFallback(r.Context(), req, reqID, func(delta string) error {
			text.WriteString(delta)
			return nil
		})
	}
	if err != nil {
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   providerName(model, path),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.Alias,
			Stream:     false,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
		return err
	}
	content := text.String()
	if res.ReasoningSignaled && !res.ReasoningVisible {
		content = codexReasoningPlaceholder() + content
	}
	resp := ChatResponse{
		ID:                reqID,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             model.Alias,
		SystemFingerprint: systemFingerprint,
		Choices: []ChatChoice{{
			Index: 0,
			Message: ChatMessage{
				Role:    "assistant",
				Content: json.RawMessage(strconv.Quote(content)),
			},
			FinishReason: res.FinishReason,
		}},
		Usage: &res.Usage,
	}
	writeJSON(w, http.StatusOK, resp)
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", res.Usage.PromptTokens),
		slog.Int("completion_tokens", res.Usage.CompletionTokens),
		slog.Int("cache_read_tokens", res.Usage.CachedTokens()),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", false),
		slog.String("backend", "codex"),
		slog.Bool("reasoning_signaled", res.ReasoningSignaled),
		slog.Bool("reasoning_visible", res.ReasoningVisible),
	)
	chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
		Stage:           chatemit.RequestStageCompleted,
		Provider:        providerName(model, path),
		Backend:         model.Backend,
		RequestID:       reqID,
		Alias:           model.Alias,
		ModelID:         model.Alias,
		Stream:          false,
		FinishReason:    res.FinishReason,
		TokensIn:        res.Usage.PromptTokens,
		TokensOut:       res.Usage.CompletionTokens,
		CacheReadTokens: res.Usage.CachedTokens(),
		DurationMs:      time.Since(started).Milliseconds(),
	})
	return nil
}

func (s *Server) streamCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	path := "direct"
	s.emitRequestStarted(r.Context(), model, path, reqID, model.Alias, true)
	sw, err := newSSEWriter(w)
	if err != nil {
		return err
	}
	sw.writeSSEHeaders()
	s.emitRequestStreamOpened(r.Context(), model, path, reqID, model.Alias, true)
	created := time.Now().Unix()
	emit := func(chunk StreamChunk) error { return sw.emitStreamChunk(systemFingerprint, chunk) }
	emitDelta := func(delta string) error {
		return emit(StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model.Alias,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: StreamDelta{Content: delta},
			}},
		})
	}

	res, _, managed, runErr := s.runCodexManaged(r.Context(), req, model, effort, reqID, emitDelta)
	if managed {
		path = "app"
	}
	if !managed && runErr == nil {
		res, runErr = s.runCodexDirect(r.Context(), req, model, effort, reqID, emitDelta)
	}
	if runErr != nil && !managed && s.cfg.Codex.AppFallback {
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   providerName(model, "direct"),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.Alias,
			Stream:     true,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        runErr.Error(),
		})
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.codex.fallback.escalating",
			slog.String("request_id", reqID),
			slog.Any("err", runErr),
		)
		path = "app"
		s.emitRequestStarted(r.Context(), model, path, reqID, model.Alias, true)
		s.emitRequestStreamOpened(r.Context(), model, path, reqID, model.Alias, true)
		res, runErr = s.runCodexAppFallback(r.Context(), req, reqID, emitDelta)
	}
	if runErr != nil {
		chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
			Stage:      chatemit.RequestStageFailed,
			Provider:   providerName(model, path),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.Alias,
			Stream:     true,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        runErr.Error(),
		})
		return runErr
	}
	if res.ReasoningSignaled && !res.ReasoningVisible {
		_ = emit(StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model.Alias,
			Choices: []StreamChoice{{
				Index: 0,
				Delta: StreamDelta{Content: codexReasoningPlaceholder()},
			}},
		})
	}
	_ = sw.emitStreamChunk(systemFingerprint, StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model.Alias,
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        StreamDelta{},
			FinishReason: &res.FinishReason,
		}},
	})
	if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
		_ = sw.emitStreamChunk(systemFingerprint, StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model.Alias,
			Choices: []StreamChoice{},
			Usage:   &res.Usage,
		})
	}
	_ = sw.writeStreamDone()
	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed",
		slog.String("request_id", reqID),
		slog.String("model", model.Alias),
		slog.Int("prompt_tokens", res.Usage.PromptTokens),
		slog.Int("completion_tokens", res.Usage.CompletionTokens),
		slog.Int("cache_read_tokens", res.Usage.CachedTokens()),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", true),
		slog.String("backend", "codex"),
		slog.Bool("reasoning_signaled", res.ReasoningSignaled),
		slog.Bool("reasoning_visible", res.ReasoningVisible),
	)
	chatemit.LogTerminal(s.log, r.Context(), s.deps.RequestEvents, chatemit.RequestEvent{
		Stage:           chatemit.RequestStageCompleted,
		Provider:        providerName(model, path),
		Backend:         model.Backend,
		RequestID:       reqID,
		Alias:           model.Alias,
		ModelID:         model.Alias,
		Stream:          true,
		FinishReason:    res.FinishReason,
		TokensIn:        res.Usage.PromptTokens,
		TokensOut:       res.Usage.CompletionTokens,
		CacheReadTokens: res.Usage.CachedTokens(),
		DurationMs:      time.Since(started).Milliseconds(),
	})
	return nil
}

func (s *Server) dispatchCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string) {
	started := time.Now()
	if req.Stream {
		if err := s.streamCodex(w, r, req, model, effort, reqID, started); err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	}
	if err := s.collectCodex(w, r, req, model, effort, reqID, started); err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
	}
}
