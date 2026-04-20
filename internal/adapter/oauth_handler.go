package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/chatemit"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

// handleOAuth fulfils a chat completion using the direct HTTP
// anthropic.Client (Bearer token from the oauth manager). Streaming
// and non-streaming responses mirror the fallback CLI path shape for
// OpenAI-compatible clients.
//
// When escalate is true the function returns a non-nil error
// without writing the response on transport failures, letting the
// dispatcher trigger the fallback. When escalate is false the
// function writes the error to w (preserving the original behavior)
// and returns nil.
func (s *Server) handleOAuth(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, escalate bool) error {
	if s.anthr == nil {
		if err := chatemit.EscalateOrWrite(
			fmt.Errorf("oauth_unconfigured: adapter built without anthropic client"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"oauth_unconfigured",
			"adapter built without anthropic client; set adapter.direct_oauth=true and restart",
		); err != nil {
			return err
		}
		return nil
	}
	if err := s.acquire(r.Context()); err != nil {
		if err2 := chatemit.EscalateOrWrite(
			fmt.Errorf("rate_limited: %w", err),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusTooManyRequests,
			"rate_limited",
			err.Error(),
		); err2 != nil {
			return err2
		}
		return nil
	}
	defer s.release()

	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	anthReq, err := s.buildAnthropicWire(req, model, effort, jsonSpec)
	if err != nil {
		if err2 := chatemit.EscalateOrWrite(
			fmt.Errorf("oauth_translate: %w", err),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadRequest,
			"invalid_request",
			err.Error(),
		); err2 != nil {
			return err2
		}
		return nil
	}

	started := time.Now()
	if req.Stream {
		// Always emit the final usage chunk; many OpenAI-compat clients
		// (Cursor, etc.) read per-turn token counts from it without setting
		// stream_options.include_usage.
		_ = req.StreamOptions
		return s.streamOAuth(w, r, anthReq, model, reqID, started, escalate, true)
	}
	return s.collectOAuth(w, r.Context(), anthReq, model, reqID, started, jsonSpec, escalate)
}

// buildAnthropicWire maps the OpenAI chat request to a native messages body
// via tooltrans, then applies thinking and effort knobs that are not part of
// the OpenAI wire shape.
func (s *Server) buildAnthropicWire(req ChatRequest, model ResolvedModel, effort string, jsonSpec JSONResponseSpec) (anthropic.Request, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return anthropic.Request{}, err
	}
	var oaReq tooltrans.OpenAIRequest
	if err := json.Unmarshal(raw, &oaReq); err != nil {
		return anthropic.Request{}, err
	}
	maxTok := anthropicMaxTokens(req.MaxTokens)
	if model.MaxOutputTokens > 0 && maxTok > model.MaxOutputTokens {
		maxTok = model.MaxOutputTokens
	}
	tr, err := tooltrans.TranslateRequest(oaReq, s.anthr.SystemPromptPrefix(), maxTok)
	if err != nil {
		return anthropic.Request{}, err
	}
	if instr := jsonSpec.SystemPrompt(false); instr != "" {
		if tr.System == "" {
			tr.System = instr
		} else {
			tr.System = tr.System + "\n\n" + instr
		}
	}
	prefix := s.anthr.SystemPromptPrefix()
	if !strings.HasPrefix(tr.System, prefix) {
		if tr.System == "" {
			tr.System = prefix
		} else {
			tr.System = prefix + "\n\n" + tr.System
		}
	}

	// Body-side billing line: required by the upstream identity check.
	cliVersion := anthropic.VersionFromUserAgent(s.anthr.UserAgent())
	if cliVersion == "" {
		cliVersion = s.anthr.CCVersion()
	}
	entry := s.anthr.CCEntrypoint()
	billingHeader := anthropic.BuildAttributionHeader(cliVersion, entry)
	// CLYDE_PROBE_BILLING mutates the billing line for debugging.
	billingHeader = mutateBillingForProbe(billingHeader, cliVersion, entry)
	if billingHeader != "" {
		tr.System = billingHeader + "\n" + tr.System
	}

	out := toAnthropicAPIRequest(tr, stripContextSuffix(model.ClaudeModel))
	// Per-model anthropic-beta extras from configured suffix map.
	out.ExtraBetas = derivePerRequestBetas(model, s.cfg.ClientIdentity.PerContextBetas)
	if effort != "" && len(model.Efforts) > 0 {
		out.OutputConfig = &anthropic.OutputConfig{Effort: effort}
	}
	switch model.Thinking {
	case ThinkingAdaptive:
		out.Thinking = &anthropic.Thinking{Type: "adaptive"}
	case ThinkingEnabled:
		budget := model.MaxOutputTokens - 1
		if budget <= 0 {
			budget = 8000
		}
		out.Thinking = &anthropic.Thinking{
			Type:         "enabled",
			BudgetTokens: budget,
		}
	case ThinkingDisabled:
		out.Thinking = &anthropic.Thinking{Type: "disabled"}
	}
	return out, nil
}

func toAnthropicAPIRequest(tr tooltrans.AnthRequest, claudeModel string) anthropic.Request {
	msgs := make([]anthropic.Message, 0, len(tr.Messages))
	for _, m := range tr.Messages {
		blocks := make([]anthropic.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			var src *anthropic.ImageSource
			if b.Source != nil {
				src = &anthropic.ImageSource{
					Type:      b.Source.Type,
					MediaType: b.Source.MediaType,
					Data:      b.Source.Data,
					URL:       b.Source.URL,
				}
			}
			blocks = append(blocks, anthropic.ContentBlock{
				Type:      b.Type,
				Text:      b.Text,
				ID:        b.ID,
				Name:      b.Name,
				Input:     b.Input,
				ToolUseID: b.ToolUseID,
				Content:   b.ResultContent,
				Source:    src,
			})
		}
		msgs = append(msgs, anthropic.Message{Role: m.Role, Content: blocks})
	}
	tools := make([]anthropic.Tool, 0, len(tr.Tools))
	for _, t := range tr.Tools {
		tools = append(tools, anthropic.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	var tc *anthropic.ToolChoice
	if tr.ToolChoice != nil {
		tc = &anthropic.ToolChoice{
			Type:                   tr.ToolChoice.Type,
			Name:                   tr.ToolChoice.Name,
			DisableParallelToolUse: tr.ToolChoice.DisableParallelToolUse,
		}
	}
	return anthropic.Request{
		Model:      claudeModel,
		System:     tr.System,
		Messages:   msgs,
		MaxTokens:  tr.MaxTokens,
		Stream:     false,
		Tools:      tools,
		ToolChoice: tc,
	}
}

// streamEventToTranslatorSSE maps decoded native stream signals to the JSON
// payloads tooltrans.StreamTranslator.HandleEvent expects for SSE event names
// (content_block_start, content_block_delta, content_block_stop). Raw SSE is
// decoded first; this layer re-encodes the subset the translator consumes.
func streamEventToTranslatorSSE(ev anthropic.StreamEvent) (eventName string, payload []byte, ok bool) {
	switch ev.Kind {
	case "text":
		p := struct {
			Index int `json:"index"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}{
			Index: ev.BlockIndex,
			Delta: struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{Type: "text_delta", Text: ev.Text},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_delta", b, true
	case "tool_use_start":
		p := struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}{
			Index: ev.BlockIndex,
			ContentBlock: struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}{Type: "tool_use", ID: ev.ToolUseID, Name: ev.ToolUseName},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_start", b, true
	case "tool_use_arg_delta":
		p := struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}{
			Index: ev.BlockIndex,
			Delta: struct {
				Type        string `json:"type"`
				PartialJSON string `json:"partial_json"`
			}{Type: "input_json_delta", PartialJSON: ev.PartialJSON},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_delta", b, true
	case "tool_use_stop":
		p := struct {
			Index int `json:"index"`
		}{Index: ev.BlockIndex}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_stop", b, true
	case "thinking":
		if ev.Text != "" {
			p := struct {
				Index int `json:"index"`
				Delta struct {
					Type     string `json:"type"`
					Thinking string `json:"thinking"`
				} `json:"delta"`
			}{
				Index: ev.BlockIndex,
				Delta: struct {
					Type     string `json:"type"`
					Thinking string `json:"thinking"`
				}{Type: "thinking_delta", Thinking: ev.Text},
			}
			b, err := json.Marshal(p)
			if err != nil {
				return "", nil, false
			}
			return "content_block_delta", b, true
		}
		p := struct {
			Index        int `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}{
			Index: ev.BlockIndex,
			ContentBlock: struct {
				Type string `json:"type"`
			}{Type: "thinking"},
		}
		b, err := json.Marshal(p)
		if err != nil {
			return "", nil, false
		}
		return "content_block_start", b, true
	default:
		return "", nil, false
	}
}

// runOAuthTranslatorStream drives tooltrans.StreamTranslator from decoded
// StreamEvents. Both collect and stream paths share this; collect buffers
// chunks while stream writes SSE frames.
func (s *Server) runOAuthTranslatorStream(
	ctx context.Context,
	anthReq anthropic.Request,
	model ResolvedModel,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (anthropic.Usage, string, string, error) {
	tr := tooltrans.NewStreamTranslator(reqID, model.Alias)
	msgStartPayload, err := json.Marshal(struct {
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}{})
	if err != nil {
		return anthropic.Usage{}, "", "", err
	}
	msgStartChunks, _, _, _, err := tr.HandleEvent("message_start", msgStartPayload)
	if err != nil {
		return anthropic.Usage{}, "", "", err
	}
	for _, ch := range msgStartChunks {
		if err := emit(ch); err != nil {
			return anthropic.Usage{}, "", "", err
		}
	}

	var streamStopReason string
	anthUsage, _, err := s.anthr.StreamEvents(ctx, anthReq, func(ev anthropic.StreamEvent) error {
		if ev.Kind == "stop" {
			streamStopReason = ev.StopReason
			return nil
		}
		evName, payload, ok := streamEventToTranslatorSSE(ev)
		if !ok {
			return nil
		}
		outChunks, _, _, _, handleErr := tr.HandleEvent(evName, payload)
		if handleErr != nil {
			return handleErr
		}
		for _, ch := range outChunks {
			if err := emit(ch); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}

	mdPayload, err := json.Marshal(struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}{
		Delta: struct {
			StopReason string `json:"stop_reason"`
		}{StopReason: streamStopReason},
		Usage: struct {
			OutputTokens int `json:"output_tokens"`
		}{OutputTokens: anthUsage.OutputTokens},
	})
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	mdChunks, _, _, _, err := tr.HandleEvent("message_delta", mdPayload)
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	for _, ch := range mdChunks {
		if err := emit(ch); err != nil {
			return anthUsage, streamStopReason, "", err
		}
	}

	stopChunks, _, finishReason, _, err := tr.HandleEvent("message_stop", []byte("{}"))
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	for _, ch := range stopChunks {
		if err := emit(ch); err != nil {
			return anthUsage, streamStopReason, "", err
		}
	}
	return anthUsage, streamStopReason, finishReason, nil
}

func (s *Server) collectOAuth(w http.ResponseWriter, ctx context.Context, req anthropic.Request, model ResolvedModel, reqID string, started time.Time, jsonSpec JSONResponseSpec, escalate bool) error {
	var buf []tooltrans.OpenAIStreamChunk
	var notice *anthropic.Notice
	emit := func(ch tooltrans.OpenAIStreamChunk) error {
		buf = append(buf, ch)
		return nil
	}
	req.OnHeaders = func(h http.Header) {
		notice = chatemit.EvaluateNoticeFromHeaders(h, s.cfg.Notices.EnabledOrDefault(), Claim)
	}
	anthUsage, anthStopReason, finishReason, err := s.runOAuthTranslatorStream(ctx, req, model, reqID, emit)
	if err != nil {
		chatemit.LogFailed(s.log, ctx, chatemit.FailedAttrs{
			Backend:    "anthropic",
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    req.Model,
			Err:        err,
			DurationMs: time.Since(started).Milliseconds(),
		})
		errMsg := err.Error()
		if notice != nil {
			if escalate {
				// We are about to retry on another backend; release the
				// notice slot so a successful retry can still deliver it.
				Unclaim(notice.Kind, notice.ResetsAt)
				s.log.LogAttrs(ctx, slog.LevelDebug, "adapter.notice.unclaimed_on_escalate",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", notice.Kind),
				)
			} else {
				errMsg = errMsg + " · " + notice.Text
				s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.notice.injected_into_error",
					slog.String("subcomponent", "adapter"),
					slog.String("request_id", reqID),
					slog.String("alias", model.Alias),
					slog.String("kind", notice.Kind),
					slog.String("notice_text", notice.Text),
				)
			}
		}
		if err := chatemit.EscalateOrWrite(
			err,
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadGateway,
			"upstream_error",
			errMsg,
		); err != nil {
			return err
		}
		return nil
	}
	u := Usage{
		PromptTokens:     anthUsage.InputTokens,
		CompletionTokens: anthUsage.OutputTokens,
		TotalTokens:      anthUsage.InputTokens + anthUsage.OutputTokens,
	}
	resp := mergeOAuthStreamChunks(reqID, model.Alias, buf, u, finishReason, jsonSpec, anthStopReason)
	resp, _ = chatemit.NoticeForResponseHeaders(resp, notice, Unclaim, json.Marshal)
	writeJSON(w, http.StatusOK, resp)
	chatemit.LogCompleted(s.log, ctx, chatemit.CompletedAttrs{
		Backend:      "anthropic",
		RequestID:    reqID,
		Alias:        model.Alias,
		ModelID:      req.Model,
		FinishReason: finishReason,
		TokensIn:     u.PromptTokens,
		TokensOut:    u.CompletionTokens,
		DurationMs:   time.Since(started).Milliseconds(),
		Stream:       false,
	})
	return nil
}

// streamOAuth honors the escalate flag for the *initial* call to
// s.anthr.StreamEvents. Once any byte has been written to the SSE stream
// the function commits and never escalates (the response headers
// are already flushed and the dispatcher cannot retry without
// confusing the OpenAI client).
func (s *Server) streamOAuth(w http.ResponseWriter, r *http.Request, req anthropic.Request, model ResolvedModel, reqID string, started time.Time, escalate bool, includeUsage bool) error {
	sw, err := newSSEWriter(w)
	if err != nil {
		if err := chatemit.EscalateOrWrite(
			fmt.Errorf("no_flusher: streaming not supported by transport"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"no_flusher",
			err.Error(),
		); err != nil {
			return err
		}
		return nil
	}

	// Flush SSE headers immediately so clients (e.g. Cursor) get a
	// response committal before we wait for the upstream's first byte.
	// Large prompts spend ~1-3s on TTFT; without an early flush, strict
	// streaming clients close the connection on timeout.
	sw.writeSSEHeaders()

	emit := func(chunk StreamChunk) error {
		return sw.emitStreamChunk(systemFingerprint, chunk)
	}

	emitTool := func(och tooltrans.OpenAIStreamChunk) error {
		return emit(streamChunkFromTooltrans(och))
	}
	req.OnHeaders = func(h http.Header) {
		notice, err := chatemit.NoticeForStreamHeaders(
			reqID,
			model.Alias,
			h,
			s.cfg.Notices.EnabledOrDefault(),
			func(chunk tooltrans.OpenAIStreamChunk) error {
				return emitTool(chunk)
			},
			Claim,
			Unclaim,
		)
		if err != nil && notice != nil {
			s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.notice.stream_emit_failed",
				slog.String("request_id", reqID),
				slog.String("alias", model.Alias),
				slog.String("model", req.Model),
				slog.String("kind", notice.Kind),
			)
		}
	}

	anthUsage, _, finishReason, err := s.runOAuthTranslatorStream(r.Context(), req, model, reqID, func(ch tooltrans.OpenAIStreamChunk) error {
		return emitTool(ch)
	})
	if err != nil {
		s.log.LogAttrs(r.Context(), slog.LevelWarn, "adapter.chat.stream_error",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.Any("err", err),
		)
		if escalate && !sw.hasCommittedHeaders() {
			return err
		}
	}

	fr := finishReason
	_ = chatemit.EmitFinishChunk(emit, reqID, model.Alias, time.Now().Unix(), fr)

	finalUsage := Usage{
		PromptTokens:     anthUsage.InputTokens,
		CompletionTokens: anthUsage.OutputTokens,
		TotalTokens:      anthUsage.InputTokens + anthUsage.OutputTokens,
	}
	if includeUsage {
		_ = chatemit.EmitUsageChunk(emit, reqID, model.Alias, time.Now().Unix(), finalUsage)
	}
	_ = sw.writeStreamDone()

	chatemit.LogCompleted(s.log, r.Context(), chatemit.CompletedAttrs{
		Backend:      "anthropic",
		RequestID:    reqID,
		Alias:        model.Alias,
		ModelID:      req.Model,
		FinishReason: fr,
		TokensIn:     finalUsage.PromptTokens,
		TokensOut:    finalUsage.CompletionTokens,
		DurationMs:   time.Since(started).Milliseconds(),
		Stream:       true,
	})
	return nil
}

// mutateBillingForProbe applies CLYDE_PROBE_BILLING for debugging.
// canonical includes cc_version, cc_entrypoint, and cch. Returns ""
// to omit the line entirely.
func mutateBillingForProbe(canonical, cliVersion, ccEntrypoint string) string {
	mode := strings.TrimSpace(os.Getenv("CLYDE_PROBE_BILLING"))
	if mode == "" {
		return canonical
	}
	const prefix = "x-anthropic-billing-header: "
	switch mode {
	case "omit":
		return ""
	case "wrong_fp":
		return prefix + "cc_version=" + cliVersion + ".zzz; cc_entrypoint=" + ccEntrypoint + "; cch=00000;"
	case "omit_fp":
		return prefix + "cc_version=" + cliVersion + "; cc_entrypoint=" + ccEntrypoint + "; cch=00000;"
	case "bad_entrypoint":
		fp := extractFingerprint(canonical)
		cchVal := extractBillingCCH(canonical)
		if cchVal == "" {
			cchVal = "00000"
		}
		return prefix + "cc_version=" + cliVersion + "." + fp + "; cc_entrypoint=garbage; cch=" + cchVal + ";"
	case "omit_entrypoint":
		fp := extractFingerprint(canonical)
		cchVal := extractBillingCCH(canonical)
		if cchVal == "" {
			cchVal = "00000"
		}
		return prefix + "cc_version=" + cliVersion + "." + fp + "; cch=" + cchVal + ";"
	case "cch_zero":
		return replaceBillingCCH(canonical, "00000")
	case "cch_z":
		return replaceBillingCCH(canonical, "ZZZZZ")
	case "cch_long":
		return replaceBillingCCH(canonical, strings.Repeat("a", 32))
	default:
		// Unknown mode: ship canonical so a typo doesn't silently
		// drop the bucket signal.
		return canonical
	}
}

// replaceBillingCCH swaps the value after `cch=` up to the next `;`.
func replaceBillingCCH(line, newVal string) string {
	const marker = "cch="
	before, after, ok := strings.Cut(line, marker)
	if !ok {
		return line + " cch=" + newVal + ";"
	}
	_, tail, ok2 := strings.Cut(after, ";")
	if !ok2 {
		return before + marker + newVal
	}
	return before + marker + newVal + ";" + tail
}

// extractBillingCCH returns the cch hex token or "" if absent.
func extractBillingCCH(line string) string {
	const marker = "cch="
	_, after, ok := strings.Cut(line, marker)
	if !ok {
		return ""
	}
	val, _, _ := strings.Cut(after, ";")
	return val
}

// extractFingerprint returns the 3-char fp suffix from a canonical
// billing line. Tolerates absence by returning "".
func extractFingerprint(line string) string {
	const verPrefix = "cc_version="
	_, rest, ok := strings.Cut(line, verPrefix)
	if !ok {
		return ""
	}
	verPart, _, ok2 := strings.Cut(rest, ";")
	if !ok2 {
		return ""
	}
	dot := strings.LastIndexByte(verPart, '.')
	if dot < 0 {
		return ""
	}
	return verPart[dot+1:]
}

func derivePerRequestBetas(model ResolvedModel, perCtx map[string]string) []string {
	if len(perCtx) == 0 {
		return nil
	}
	var out []string
	for suffix, beta := range perCtx {
		if beta == "" {
			continue
		}
		if strings.Contains(model.ClaudeModel, suffix) {
			out = append(out, beta)
		}
	}
	return out
}

// stripContextSuffix removes a bracketed wire suffix from the model id.
func stripContextSuffix(model string) string {
	if i := strings.Index(model, "["); i > 0 {
		return model[:i]
	}
	return model
}

// anthropicMaxTokens picks a max_tokens value: caller-supplied when
// positive, otherwise the package default.
func anthropicMaxTokens(req *int) int {
	if req != nil && *req > 0 {
		return *req
	}
	return anthropic.MaxOutputTokens
}
