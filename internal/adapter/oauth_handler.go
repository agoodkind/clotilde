package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

// handleOAuth fulfils a chat completion using the direct
// anthropic.Client (Bearer auth against /v1/messages). It mirrors
// the shape of the streaming and non-streaming responses produced
// by the legacy `claude -p` path so OpenAI-compatible clients see
// no observable difference between backends.
//
// When escalate is true the function returns a non-nil error
// without writing the response on transport failures, letting the
// dispatcher trigger the fallback. When escalate is false the
// function writes the error to w (preserving the original behavior)
// and returns nil.
func (s *Server) handleOAuth(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, escalate bool) error {
	if s.anthr == nil {
		if escalate {
			return fmt.Errorf("oauth_unconfigured: adapter built without anthropic client")
		}
		writeError(w, http.StatusInternalServerError, "oauth_unconfigured",
			"adapter built without anthropic client; set adapter.direct_oauth=true and restart")
		return nil
	}
	if err := s.acquire(r.Context()); err != nil {
		if escalate {
			return fmt.Errorf("rate_limited: %w", err)
		}
		writeError(w, http.StatusTooManyRequests, "rate_limited", err.Error())
		return nil
	}
	defer s.release()

	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	anthReq, err := s.buildAnthropicWire(req, model, effort, jsonSpec)
	if err != nil {
		if escalate {
			return fmt.Errorf("oauth_translate: %w", err)
		}
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return nil
	}

	started := time.Now()
	if req.Stream {
		return s.streamOAuth(w, r, anthReq, model, reqID, started, escalate)
	}
	return s.collectOAuth(w, r.Context(), anthReq, model, reqID, started, jsonSpec, escalate)
}

// buildAnthropicWire maps the OpenAI chat request to an Anthropic /v1/messages
// body via tooltrans, then applies thinking and effort knobs that are not part
// of the OpenAI wire shape.
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

	// OAuth bucket impersonation: the official claude-cli smuggles an
	// x-anthropic-billing-header line in as system[0] because the OAuth
	// path strips arbitrary x-* HTTP headers. Without this token in the
	// request body the upstream classifies the request as
	// non-CLI traffic and returns 429 immediately even with the full
	// CLI header set.
	//
	// The version suffix on cc_version is a SHA256-derived 3-char
	// fingerprint of (salt + sample-of-first-user-message + version) per
	// the CLI's fingerprint.ts. Recompute it per request using the same
	// algorithm so the value stays valid if/when Anthropic enforces the
	// hash on the OAuth path.
	//
	// See docs/openai-adapter.md "OAuth bucket impersonation drift" /
	// "Bisection results" for the captured evidence and full algorithm.
	cliVersion := anthropic.VersionFromUserAgent(s.anthr.UserAgent())
	if cliVersion == "" {
		cliVersion = "2.1.114"
	}
	billingHeader := anthropic.BuildAttributionHeader(firstUserMessageText(tr.Messages), cliVersion, "sdk-cli")
	tr.System = billingHeader + "\n" + tr.System

	out := toAnthropicAPIRequest(tr, stripContextSuffix(model.ClaudeModel))
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

// streamEventToTranslatorSSE maps decoded Anthropic stream signals to the JSON
// payloads tooltrans.StreamTranslator.HandleEvent expects for SSE event names
// (content_block_start, content_block_delta, content_block_stop). Anthropic's
// client decodes raw SSE first; this layer re-encodes the subset the
// translator consumes.
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

// runOAuthTranslatorStream drives tooltrans.StreamTranslator from Anthropic
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

	_, _, finishReason, _, err := tr.HandleEvent("message_stop", []byte("{}"))
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	return anthUsage, streamStopReason, finishReason, nil
}

func (s *Server) collectOAuth(w http.ResponseWriter, ctx context.Context, req anthropic.Request, model ResolvedModel, reqID string, started time.Time, jsonSpec JSONResponseSpec, escalate bool) error {
	var buf []tooltrans.OpenAIStreamChunk
	emit := func(ch tooltrans.OpenAIStreamChunk) error {
		buf = append(buf, ch)
		return nil
	}
	anthUsage, _, finishReason, err := s.runOAuthTranslatorStream(ctx, req, model, reqID, emit)
	if err != nil {
		s.log.LogAttrs(ctx, slog.LevelError, "adapter.chat.failed",
			slog.String("backend", "anthropic"),
			slog.String("request_id", reqID),
			slog.String("alias", model.Alias),
			slog.String("model", req.Model),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
			slog.Any("err", err),
		)
		if escalate {
			return err
		}
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		return nil
	}
	u := Usage{
		PromptTokens:     anthUsage.InputTokens,
		CompletionTokens: anthUsage.OutputTokens,
		TotalTokens:      anthUsage.InputTokens + anthUsage.OutputTokens,
	}
	resp := mergeOAuthStreamChunks(reqID, model.Alias, buf, u, finishReason, jsonSpec)
	writeJSON(w, http.StatusOK, resp)
	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.chat.completed",
		slog.String("backend", "anthropic"),
		slog.String("request_id", reqID),
		slog.String("alias", model.Alias),
		slog.String("model", req.Model),
		slog.Int("tokens_in", u.PromptTokens),
		slog.Int("tokens_out", u.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", false),
	)
	return nil
}

// streamOAuth honors the escalate flag for the *initial* call to
// s.anthr.StreamEvents. Once any byte has been written to the SSE stream
// the function commits and never escalates (the response headers
// are already flushed and the dispatcher cannot retry without
// confusing the OpenAI client).
func (s *Server) streamOAuth(w http.ResponseWriter, r *http.Request, req anthropic.Request, model ResolvedModel, reqID string, started time.Time, escalate bool) error {
	sw, err := newSSEWriter(w)
	if err != nil {
		if escalate {
			return fmt.Errorf("no_flusher: streaming not supported by transport")
		}
		writeError(w, http.StatusInternalServerError, "no_flusher", err.Error())
		return nil
	}

	emit := func(chunk StreamChunk) error {
		return sw.emitStreamChunk(systemFingerprint, chunk)
	}

	emitTool := func(och tooltrans.OpenAIStreamChunk) error {
		return emit(streamChunkFromTooltrans(och))
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
	_ = emit(StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model.Alias,
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        StreamDelta{},
			FinishReason: &fr,
		}},
	})

	finalUsage := Usage{
		PromptTokens:     anthUsage.InputTokens,
		CompletionTokens: anthUsage.OutputTokens,
		TotalTokens:      anthUsage.InputTokens + anthUsage.OutputTokens,
	}
	if req.Stream {
		_ = emit(StreamChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model.Alias,
			Choices: []StreamChoice{},
			Usage:   &finalUsage,
		})
	}
	_ = sw.writeStreamDone()

	s.log.LogAttrs(r.Context(), slog.LevelInfo, "adapter.chat.completed",
		slog.String("backend", "anthropic"),
		slog.String("request_id", reqID),
		slog.String("alias", model.Alias),
		slog.String("model", req.Model),
		slog.Int("tokens_in", finalUsage.PromptTokens),
		slog.Int("tokens_out", finalUsage.CompletionTokens),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		slog.Bool("stream", true),
	)
	return nil
}

// stripContextSuffix removes the [1m] suffix the registry uses to
// distinguish 1M context Claude snapshots from their 200k siblings.
// Anthropic's API takes only the bare model id.
func stripContextSuffix(model string) string {
	if i := strings.Index(model, "["); i > 0 {
		return model[:i]
	}
	return model
}

// firstUserMessageText returns the concatenated text of the first
// user-role message's text content blocks, or "" if there is no user
// message or it has no text. Used to seed the attribution-header
// fingerprint, which the official CLI computes from the first user
// message body. See internal/adapter/anthropic/fingerprint.go.
func firstUserMessageText(messages []tooltrans.AnthMessage) string {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		var b strings.Builder
		for _, block := range m.Content {
			if block.Type == "text" && block.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return ""
}

// anthropicMaxTokens picks a max_tokens value: caller-supplied when
// positive, otherwise the package default.
func anthropicMaxTokens(req *int) int {
	if req != nil && *req > 0 {
		return *req
	}
	return anthropic.MaxOutputTokens
}
